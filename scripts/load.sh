#!/usr/bin/env bash
# Sinh tải, để quan sát HPA co giãn, làm đầy các panel Grafana và tạo đủ trace
# cho service graph của Tempo hiện ra.
#
#   ./scripts/load.sh 300 10     # 300 giây, 10 request mỗi giây
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
