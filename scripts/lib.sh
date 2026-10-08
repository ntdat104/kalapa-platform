#!/usr/bin/env bash
# Các hàm dùng chung. File này để source, không phải để chạy trực tiếp.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-kalapa}"
DOMAIN="${DOMAIN:-kalapa.local}"
HOSTS=(api.kalapa.local grafana.kalapa.local argocd.kalapa.local identity.kalapa.local prometheus.kalapa.local)

info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m  ✓\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m  !\033[0m %s\n' "$*"; }
die()   { printf '\033[1;31m  ✗\033[0m %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

kube() { kubectl --context "$CLUSTER_NAME" "$@"; }

# wait_for <mô tả> <thời gian chờ tối đa tính bằng giây> <lệnh...>
# Lặp lại cho tới khi lệnh thành công. Dùng thay cho `sleep 60` để script kết
# thúc ngay khi cluster sẵn sàng, chứ không theo một lịch cố định.
wait_for() {
  local desc="$1" timeout="$2"; shift 2
  local deadline=$(( $(date +%s) + timeout ))
  printf '    waiting for %s ' "$desc"
  until "$@" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      printf ' timeout\n'
      return 1
    fi
    printf '.'
    sleep 5
  done
  printf ' ready\n'
}

repo_root() { git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel; }
