#!/usr/bin/env bash
# Creates the minikube cluster this stack is sized for.
#
# Memory maths, measured rather than guessed (see README.md for the full table):
#
#   sum of all container memory REQUESTS   ~5.4 GB
#   sum of all container memory LIMITS     ~8.4 GB
#   Kubernetes control plane itself        ~1.2 GB
#
# 8 GB is therefore the comfortable floor with every component on. At 6.5 GB the
# limits reach 105% of allocatable: the cluster still converges and runs, but a
# simultaneous restart of several components starves the node and the API server
# stops answering for a minute or two. Docker Desktop must be allocated at least
# 10 GB (Settings > Resources) for 8 GB here to fit alongside its own overhead.
#
# Short on memory? `make up-local` skips Argo CD (-1.3 GB of limits) and
# dropping Keycloak frees another 1.2 GB.
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
  # --cni=calico is what makes NetworkPolicy objects actually enforce. The
  # default bridge CNI accepts them and ignores them, so a policy you write
  # appears to work while blocking nothing. Costs ~150Mi.
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
# ingress  -> the NGINX ingress controller; every *.kalapa.local host needs it
# metrics-server -> `kubectl top` and, crucially, the HPA. Without it an HPA
#                   reports <unknown>/70% forever and never scales.
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
