#!/usr/bin/env bash
# Thêm (hoặc làm mới) các hostname của lab vào /etc/hosts. Cần quyền sudo.
source "$(dirname "$0")/lib.sh"

IP=$(minikube ip -p "$CLUSTER_NAME")
MARKER="# kalapa-platform"

info "Pointing ${HOSTS[*]} at $IP"
# Xoá khối cũ nếu có, để khi chạy lại sau một lần dựng lại cluster (lúc đó IP
# đã đổi) không còn dòng cũ che mất dòng mới.
sudo sed -i.bak "/${MARKER}/d" /etc/hosts
printf '%s %s %s\n' "$IP" "${HOSTS[*]}" "$MARKER" | sudo tee -a /etc/hosts >/dev/null
ok "/etc/hosts updated (backup at /etc/hosts.bak)"
grep "$MARKER" /etc/hosts
