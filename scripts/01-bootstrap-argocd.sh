#!/usr/bin/env bash
# Cài Argo CD và apply đúng một root Application. Từ thời điểm này trở đi, mọi
# thứ đều do Git điều khiển.
source "$(dirname "$0")/lib.sh"

ROOT=$(repo_root)
ARGOCD_CHART_VERSION="${ARGOCD_CHART_VERSION:-10.9.7}"

need helm
need kubectl

grep -q '__GIT_REPO_URL__' "$ROOT/deploy/argocd/bootstrap/root-app.yaml" \
  && die "placeholders not substituted — run scripts/init-repo.sh first"

info "Installing Argo CD (chart $ARGOCD_CHART_VERSION)"
helm repo add argo https://argoproj.github.io/argo-helm >/dev/null 2>&1 || true
helm repo update argo >/dev/null
helm upgrade --install argocd argo/argo-cd \
  --version "$ARGOCD_CHART_VERSION" \
  --namespace argocd --create-namespace \
  --values "$ROOT/deploy/argocd/bootstrap/argocd-values.yaml" \
  --wait --timeout 10m

info "Waiting for the Argo CD controllers"
wait_for "application-controller" 300 \
  kube -n argocd rollout status statefulset/argocd-application-controller --timeout=10s
wait_for "repo-server" 300 \
  kube -n argocd rollout status deployment/argocd-repo-server --timeout=10s

info "Applying the root Application"
# Lệnh `kubectl apply` DUY NHẤT trong cả quy trình. Từ đây cluster tự kéo trạng
# thái về từ Git, thay vì bị đẩy vào.
kube apply -f "$ROOT/deploy/argocd/bootstrap/root-app.yaml"
ok "root application created"

password=$(kube -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' 2>/dev/null | base64 -d || echo '(not generated yet)')

cat <<EOF

Argo CD is up. It will now pull everything else from Git.

  UI       http://argocd.kalapa.local      (after 'make hosts')
  user     admin
  password ${password}

  Watch it converge:
    watch kubectl --context ${CLUSTER_NAME} -n argocd get applications

  Expect roughly:
    wave -20  namespaces                 ~5s
    wave -10  operators (CNPG, Strimzi)  ~1m
    wave 0-2  observability stack        ~4m
    wave 10   Postgres, Kafka, Keycloak  ~5m   <- Kafka is the slow one
    wave 20   kalapa-config              ~5s
    wave 30   gateway, kyc, scoring      ~1m

  First full convergence takes 10-15 minutes, almost all of it image pulls.
EOF
