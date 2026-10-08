#!/usr/bin/env bash
# Installs the identical stack with plain Helm, no Argo CD and no GitHub.
#
# This is the escape hatch for "I have no GitHub account", "I am offline", or
# "Argo CD will not sync and I want to know whether the charts themselves are
# wrong". It is not a second source of truth: the script READS the Argo CD
# Application manifests and replays them with helm, so the two paths cannot
# drift. If you change an Application's values, this script picks it up.
#
# What you lose versus the real path: drift detection, self-healing, the sync
# wave dependency graph, and the audit trail. Everything that actually runs in
# the cluster is the same.
source "$(dirname "$0")/lib.sh"

ROOT=$(repo_root)
need helm
need kubectl
need python3

info "Namespaces"
kube apply -f "$ROOT/deploy/manifests/namespaces.yaml"

info "Adding chart repositories"
REPOS=(
  "cnpg https://cloudnative-pg.github.io/charts"
  "strimzi https://strimzi.io/charts/"
  "prometheus-community https://prometheus-community.github.io/helm-charts"
  "grafana https://grafana.github.io/helm-charts"
  "open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts"
  "stakater https://stakater.github.io/stakater-charts"
)
names=()
for entry in "${REPOS[@]}"; do
  set -- $entry
  helm repo add "$1" "$2" >/dev/null 2>&1 || warn "could not add repo $1"
  names+=("$1")
done
# Update ONLY our repos by name. A bare `helm repo update` also refreshes every
# other repo in your global Helm config, and one unreachable repo there would
# abort this script under `set -e`.
helm repo update "${names[@]}" >/dev/null 2>&1 || warn "some repos could not be refreshed"

# Turn every Application manifest into a line the shell can consume.
#
# Fields are separated by US (0x1f), not a tab. With a whitespace IFS, bash
# collapses runs of the delimiter, so a row with an empty field (a local chart
# has no version) silently shifts every column after it — which lands releases
# in the wrong namespace with no error at all. 0x1f is not whitespace, so empty
# fields survive.
#   wave <US> release <US> repo|CHART|MANIFESTS <US> chart-or-path <US> version <US> namespace <US> values-file
plan=$(mktemp)
python3 - "$ROOT" "$plan" <<'PY'
import os, sys, glob, yaml, tempfile

root, out = sys.argv[1], sys.argv[2]
rows = []
for path in sorted(glob.glob(f"{root}/deploy/argocd/platform/*.yaml")
                   + sorted(glob.glob(f"{root}/deploy/argocd/apps/*.yaml"))):
    doc = yaml.safe_load(open(path))
    if not doc or doc.get("kind") != "Application":
        continue  # skips the ApplicationSet; its services are handled below
    meta, spec = doc["metadata"], doc["spec"]
    wave = int(meta.get("annotations", {}).get("argocd.argoproj.io/sync-wave", "0"))
    src, dest = spec["source"], spec["destination"]
    release = src.get("helm", {}).get("releaseName") or meta["name"]
    ns = dest["namespace"]

    values_file = ""
    vals = src.get("helm", {}).get("values")
    if vals:
        fd, values_file = tempfile.mkstemp(suffix=f"-{release}.yaml")
        os.write(fd, vals.encode())
        os.close(fd)

    if "chart" in src:
        rows.append((wave, release, src["repoURL"], src["chart"], src.get("targetRevision", ""), ns, values_file))
    else:
        # A path-based source: a directory of plain manifests, or a local chart.
        local = os.path.join(root, src["path"])
        kind = "CHART" if os.path.exists(os.path.join(local, "Chart.yaml")) else "MANIFESTS"
        rows.append((wave, release, kind, local, "", ns, values_file))

# The ApplicationSet generates one Application per service chart; replay that.
for svc in ("gateway", "kyc", "scoring"):
    rows.append((30, svc, "CHART", f"{root}/deploy/charts/{svc}", "", "kalapa", ""))

rows.sort(key=lambda r: (r[0], r[1]))
with open(out, "w") as fh:
    for r in rows:
        fh.write("\x1f".join(str(x) for x in r) + "\n")
print(f"    {len(rows)} components planned", file=sys.stderr)
PY

current_wave=""
while IFS=$'\x1f' read -r wave release repo chart version ns values; do
  if [ "$wave" != "$current_wave" ]; then
    info "Sync wave $wave"
    current_wave="$wave"
  fi

  [ -n "$ns" ] || die "no namespace parsed for release '$release' — the plan file is malformed"

  args=(upgrade --install "$release" --namespace "$ns" --create-namespace --kube-context "$CLUSTER_NAME")
  [ -n "$values" ] && args+=(--values "$values")

  case "$repo" in
    MANIFESTS)
      kube apply -n "$ns" -f "$chart"
      ok "$release (plain manifests)"
      continue
      ;;
    CHART)
      helm dependency build "$chart" >/dev/null 2>&1 || true
      args+=("$chart")
      ;;
    *)
      args+=("$chart" --repo "$repo")
      [ -n "$version" ] && args+=(--version "$version")
      ;;
  esac

  # --wait on the heavy operators only. Waiting on everything turns a 12-minute
  # install into a 40-minute one, because Helm waits for pods that are waiting
  # for images.
  case "$release" in
    cnpg-operator|strimzi-operator) args+=(--wait --timeout 5m) ;;
  esac

  if helm "${args[@]}" >/dev/null; then
    ok "$release"
  else
    warn "$release failed — re-run with 'helm ${args[*]}' to see why"
  fi

  # The operators must be reconciling before their custom resources land.
  case "$release" in
    cnpg-operator)
      wait_for "the Cluster CRD" 180 kube get crd clusters.postgresql.cnpg.io ;;
    strimzi-operator)
      wait_for "the Kafka CRD" 180 kube get crd kafkas.kafka.strimzi.io ;;
    kube-prometheus-stack)
      wait_for "the ServiceMonitor CRD" 180 kube get crd servicemonitors.monitoring.coreos.com ;;
  esac
done < "$plan"

rm -f "$plan"

cat <<EOF

Installed without Argo CD.

  Watch it settle:   watch kubectl --context ${CLUSTER_NAME} get pods -A
  Then:              make smoke

To switch to the real GitOps path later:
  gh auth login && make init && git push && make bootstrap
Argo CD adopts the existing releases rather than reinstalling them.
EOF
