#!/usr/bin/env bash
# Removes the cluster. PVCs go with it, so Postgres and Kafka data is lost.
source "$(dirname "$0")/lib.sh"

read -r -p "Delete minikube profile '$CLUSTER_NAME' and all its data? [y/N] " reply
[[ "$reply" =~ ^[Yy]$ ]] || exit 0
minikube delete -p "$CLUSTER_NAME"
ok "cluster removed"
warn "/etc/hosts still has the kalapa entries; remove them with:"
echo "    sudo sed -i.bak '/# kalapa-platform/d' /etc/hosts"
