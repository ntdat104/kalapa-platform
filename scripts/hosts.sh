#!/usr/bin/env bash
# Adds (or refreshes) the lab hostnames in /etc/hosts. Needs sudo.
source "$(dirname "$0")/lib.sh"

IP=$(minikube ip -p "$CLUSTER_NAME")
MARKER="# kalapa-platform"

info "Pointing ${HOSTS[*]} at $IP"
# Remove any previous block so re-running after a cluster recreate (which
# changes the IP) does not leave a stale entry shadowing the new one.
sudo sed -i.bak "/${MARKER}/d" /etc/hosts
printf '%s %s %s\n' "$IP" "${HOSTS[*]}" "$MARKER" | sudo tee -a /etc/hosts >/dev/null
ok "/etc/hosts updated (backup at /etc/hosts.bak)"
grep "$MARKER" /etc/hosts
