#!/usr/bin/env bash
# Tạo cluster minikube mà toàn bộ stack này được chỉnh cho vừa.
#
# Phép tính bộ nhớ, đo thật chứ không đoán (bảng đầy đủ ở README.md):
#
#   tổng REQUESTS bộ nhớ của mọi container   ~5,4 GB
#   tổng LIMITS bộ nhớ của mọi container     ~8,4 GB
#   bản thân control plane của Kubernetes    ~1,2 GB
#
# Vậy 8 GB là ngưỡng sàn thoải mái khi bật đủ mọi thành phần. Ở mức 6,5 GB thì
# tổng limits chạm 105% phần cấp phát được: cluster vẫn hội tụ và chạy, nhưng
# khi vài thành phần restart cùng lúc thì node bị bỏ đói và API server ngừng trả
# lời một hai phút. Docker Desktop phải được cấp ít nhất 10 GB (Settings >
# Resources) thì 8 GB ở đây mới vừa, tính cả chi phí của chính nó.
#
# Eo hẹp bộ nhớ? `make up-local` bỏ qua Argo CD (giảm 1,3 GB limits) và bỏ
# Keycloak giải phóng thêm 1,2 GB nữa.
source "$(dirname "$0")/lib.sh"

MEMORY="${MEMORY:-8g}"
CPUS="${CPUS:-5}"
DISK="${DISK:-40g}"
K8S_VERSION="${K8S_VERSION:-v1.34.0}"
CNI="${CNI:-auto}"

need minikube
need kubectl
need helm

info "Checking Docker's memory allocation"
docker_mem=$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)
docker_gb=$(( docker_mem / 1024 / 1024 / 1024 ))
want_gb=${MEMORY%g}
if [ "$docker_gb" -lt $(( want_gb + 1 )) ]; then
  warn "Docker has ${docker_gb}GB but the cluster asks for ${MEMORY}."
  warn "Raise it in Docker Desktop > Settings > Resources > Memory to $(( want_gb + 2 ))GB,"
  warn "or run with a smaller profile:  PROFILE=lite make up"
  read -r -p "    Continue anyway? [y/N] " reply
  [[ "$reply" =~ ^[Yy]$ ]] || exit 1
else
  ok "Docker has ${docker_gb}GB available"
fi

if minikube status -p "$CLUSTER_NAME" >/dev/null 2>&1; then
  ok "cluster '$CLUSTER_NAME' already running"
else
  info "Starting minikube profile '$CLUSTER_NAME' (${CPUS} CPU, ${MEMORY} RAM, ${DISK} disk)"
  # --cni=calico chính là thứ khiến các object NetworkPolicy thực sự có hiệu
  # lực. CNI bridge mặc định nhận chúng rồi bỏ qua, nên một policy bạn viết trông
  # như đang chạy mà chẳng chặn gì cả. Tốn khoảng 150Mi.
  cni_flag=()
  [ "$CNI" != "auto" ] && cni_flag=(--cni="$CNI")
  minikube start \
    -p "$CLUSTER_NAME" \
    --driver=docker \
    --cpus="$CPUS" \
    --memory="$MEMORY" \
    --disk-size="$DISK" \
    --kubernetes-version="$K8S_VERSION" \
    "${cni_flag[@]}"
fi

info "Enabling addons"
# ingress  -> NGINX ingress controller; mọi host *.kalapa.local đều cần nó
# metrics-server -> cần cho `kubectl top` và, quan trọng hơn, cho HPA. Thiếu nó
#                   thì HPA báo <unknown>/70% mãi mãi và không bao giờ co giãn.
for addon in ingress metrics-server storage-provisioner default-storageclass; do
  minikube addons enable "$addon" -p "$CLUSTER_NAME" >/dev/null 2>&1 && ok "$addon"
done

info "Waiting for the ingress controller"
wait_for "ingress-nginx" 300 \
  kubectl --context "$CLUSTER_NAME" -n ingress-nginx wait --for=condition=ready pod \
    -l app.kubernetes.io/component=controller --timeout=10s

IP=$(minikube ip -p "$CLUSTER_NAME")
ok "cluster ready at $IP"

cat <<EOF

Next: point the lab hostnames at the node.

  sudo tee -a /etc/hosts >/dev/null <<HOSTS
$IP  ${HOSTS[*]}
HOSTS

Or run:  make hosts
EOF
