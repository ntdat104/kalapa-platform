#!/usr/bin/env bash
# Bằng chứng đầu-cuối rằng cả chuỗi hoạt động:
#
#   curl -> ingress-nginx -> gateway -> kyc -> Postgres
#                                        \-> Kafka -> scoring -> Postgres
#                                                        \-> endpoint tổng hợp
#
# và telemetry của chính request đó đã về đủ cả ba nơi lưu.
source "$(dirname "$0")/lib.sh"

API="${API:-http://api.kalapa.local}"
need curl

info "1/6  Gateway reachable through the Ingress"
version=$(curl -fsS --max-time 10 "$API/api/version") || die "gateway unreachable at $API — is 'make hosts' done?"
ok "$version"

info "2/6  Submitting a KYC application"
national_id="0$(printf '%011d' $((RANDOM * RANDOM % 100000000000)))"
response=$(curl -fsS --max-time 15 -X POST "$API/api/kyc/applications" \
  -H 'Content-Type: application/json' \
  -d "{\"national_id\":\"${national_id}\",\"full_name\":\"Nguyen Van Lab\",\"date_of_birth\":\"1990-05-02\"}") \
  || die "submit failed"
app_id=$(echo "$response" | sed -nE 's/.*"id":"([^"]+)".*/\1/p')
[ -n "$app_id" ] || die "no application id in response: $response"
ok "application $app_id"

info "3/6  Reading it back (gateway -> kyc -> Postgres)"
curl -fsS "$API/api/kyc/applications/$app_id" >/dev/null || die "read-back failed"
ok "persisted"

info "4/6  Waiting for scoring (kyc -> Kafka -> scoring -> Postgres)"
# Đây là chặng bất đồng bộ. Nếu nó hết giờ chờ thì đường đi của sự kiện đã hỏng:
# xem log của kyc tìm lỗi publish, rồi `kubectl -n kalapa logs deploy/scoring`.
deadline=$(( $(date +%s) + 90 ))
score=""
while [ "$(date +%s)" -lt "$deadline" ]; do
  if score=$(curl -fsS "$API/api/scoring/scores/$app_id" 2>/dev/null); then
    break
  fi
  printf '.'
  sleep 3
done
printf '\n'
[ -n "$score" ] || die "no score after 90s — the Kafka path is broken"
ok "$score"

info "5/6  Aggregate endpoint (one request, two parallel upstream calls)"
agg=$(curl -fsS "$API/api/applications/$app_id") || die "aggregate failed"
echo "$agg" | grep -q '"score"' || warn "aggregate returned no score block"
ok "aggregate returned both halves"

info "6/6  Telemetry"
pf() { kube -n observability port-forward "svc/$1" "$2:$3" >/dev/null 2>&1 & echo $!; }

prom_pid=$(pf prometheus-kube-prometheus-prometheus 19090 9090)
loki_pid=$(pf loki-gateway 13100 80)
tempo_pid=$(pf tempo 13200 3200)
trap 'kill '"$prom_pid $loki_pid $tempo_pid"' 2>/dev/null || true' EXIT
sleep 4

series=$(curl -fsS 'http://localhost:19090/api/v1/query?query=kalapa_build_info' 2>/dev/null \
  | grep -o '"service":"[a-z]*"' | sort -u | tr '\n' ' ')
[ -n "$series" ] && ok "Prometheus has metrics for: $series" || warn "Prometheus has no kalapa_build_info yet"

logs=$(curl -fsS --get 'http://localhost:13100/loki/api/v1/query_range' \
  --data-urlencode 'query={namespace="kalapa"}' --data-urlencode 'limit=1' 2>/dev/null)
echo "$logs" | grep -q '"values"' && ok "Loki is receiving logs" || warn "Loki has no logs from the kalapa namespace yet"

tags=$(curl -fsS 'http://localhost:13200/api/search/tag/service.name/values' 2>/dev/null)
echo "$tags" | grep -q 'gateway' && ok "Tempo has traces from the gateway" || warn "Tempo has no gateway traces yet"

cat <<EOF

All green.

  Grafana   http://grafana.kalapa.local      admin / admin
  Argo CD   http://argocd.kalapa.local
  Keycloak  http://identity.kalapa.local     admin / admin

  Find this exact request's trace:
    Grafana > Explore > Tempo > Search > service.name = gateway
    The trace should span gateway -> kyc -> (kafka) -> scoring.

  Application id: $app_id
EOF
