#!/usr/bin/env bash
# Cài đúng stack đó bằng Helm thuần, không Argo CD và không cần GitHub.
#
# Đây là cửa thoát hiểm cho các tình huống "tôi không có tài khoản GitHub", "tôi
# đang offline", hoặc "Argo CD không chịu đồng bộ và tôi muốn biết bản thân các
# chart có sai không". Nó KHÔNG phải nguồn sự thật thứ hai: script ĐỌC chính các
# manifest Application của Argo CD rồi phát lại bằng helm, nên hai đường không
# thể lệch nhau. Bạn đổi values của một Application thì script này nhận ngay.
#
# So với đường thật, bạn mất: phát hiện drift, tự chữa lành, đồ thị phụ thuộc
# theo sync wave, và dấu vết kiểm toán. Còn mọi thứ thực sự chạy trong cluster
# thì giống hệt.
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
# CHỈ cập nhật đúng các repo của ta, theo tên. Một lệnh `helm repo update` trần
# sẽ làm mới cả mọi repo khác trong cấu hình Helm toàn cục của bạn, và chỉ cần
# một repo không với tới được là script này đứt ngay vì `set -e`.
helm repo update "${names[@]}" >/dev/null 2>&1 || warn "some repos could not be refreshed"

# Biến mỗi manifest Application thành một dòng mà shell đọc được.
#
# Các trường ngăn cách bằng ký tự US (0x1f), không phải tab. Với IFS là ký tự
# khoảng trắng, bash gộp nhiều dấu ngăn liền nhau thành một, nên một dòng có
# trường rỗng (chart cục bộ thì không có version) sẽ âm thầm đẩy lệch mọi cột
# phía sau — khiến release được cài vào nhầm namespace mà không báo lỗi gì cả.
# 0x1f không phải khoảng trắng nên trường rỗng được giữ nguyên.
#   wave <US> release <US> repo|CHART|MANIFESTS <US> chart-hoặc-đường-dẫn <US> version <US> namespace <US> file-values
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
        # Nguồn theo đường dẫn: một thư mục manifest thuần, hoặc một chart cục bộ.
        local = os.path.join(root, src["path"])
        kind = "CHART" if os.path.exists(os.path.join(local, "Chart.yaml")) else "MANIFESTS"
        rows.append((wave, release, kind, local, "", ns, values_file))

# ApplicationSet sinh ra mỗi chart service một Application; ở đây phát lại điều đó.
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

  # Chỉ dùng --wait cho các operator nặng. Chờ mọi thứ sẽ biến một lần cài 12
  # phút thành 40 phút, vì Helm ngồi chờ những pod vốn đang chờ tải image.
  case "$release" in
    cnpg-operator|strimzi-operator) args+=(--wait --timeout 5m) ;;
  esac

  if helm "${args[@]}" >/dev/null; then
    ok "$release"
  else
    warn "$release failed — re-run with 'helm ${args[*]}' to see why"
  fi

  # Các operator phải đang điều hoà trước khi custom resource của chúng được apply.
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
