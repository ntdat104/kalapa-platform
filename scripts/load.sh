#!/usr/bin/env bash
# Generates traffic, for watching the HPA scale, filling the Grafana panels
# and producing enough traces for Tempo's service graph to render.
#
#   ./scripts/load.sh 300 10     # 300 seconds, 10 requests/second
source "$(dirname "$0")/lib.sh"

DURATION="${1:-120}"
RPS="${2:-5}"
API="${API:-http://api.kalapa.local}"

info "Driving ${RPS} req/s at $API for ${DURATION}s"
end=$(( $(date +%s) + DURATION ))
sent=0; failed=0
while [ "$(date +%s)" -lt "$end" ]; do
  for _ in $(seq 1 "$RPS"); do
    nid="0$(printf '%011d' $((RANDOM * RANDOM % 100000000000)))"
    curl -fsS --max-time 5 -X POST "$API/api/kyc/applications" \
      -H 'Content-Type: application/json' \
      -d "{\"national_id\":\"${nid}\",\"full_name\":\"Load Test\"}" >/dev/null 2>&1 \
      || failed=$(( failed + 1 ))
    sent=$(( sent + 1 ))
  done
  printf '\r    sent %d, failed %d' "$sent" "$failed"
  sleep 1
done
printf '\n'
ok "done — $sent requests, $failed failures"
