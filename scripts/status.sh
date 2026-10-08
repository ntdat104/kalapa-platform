#!/usr/bin/env bash
# One screen that answers "what is actually wrong".
source "$(dirname "$0")/lib.sh"

info "Argo CD applications"
kube -n argocd get applications -o custom-columns=\
'NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status,MESSAGE:.status.conditions[0].message' 2>/dev/null \
  || warn "Argo CD is not installed yet"

info "Pods that are not Running/Completed"
kube get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded 2>/dev/null \
  | grep -v '^NAMESPACE' || ok "every pod is Running or Completed"

info "Recent warnings"
kube get events -A --field-selector type=Warning \
  --sort-by=.lastTimestamp 2>/dev/null | tail -12 || true

info "Memory by namespace"
kube top pods -A --no-headers 2>/dev/null \
  | awk '{ns[$1]+=$4} END {for (n in ns) printf "  %-18s %5d Mi\n", n, ns[n]}' \
  | sort -k2 -rn \
  || warn "metrics-server is not ready yet"

info "Node pressure"
kube describe node 2>/dev/null | grep -A6 'Allocated resources' | head -10 || true
