# 07 — Bài lab

Mười hai bài tập. Mỗi bài cố tình làm hỏng một thứ, hoặc xây một thứ mà bản
nền bỏ trống. Làm theo thứ tự; bài sau giả định bài trước.

Mỗi bài đều nêu thứ bạn nên *dự đoán* trước khi chạy bất cứ gì. Việc dự đoán
trước mới là phần lớn giá trị — bị bất ngờ chính là cách bạn phát hiện mình đã
thực sự tin điều gì.

---

## Lab 1 — Probe, và mỗi kiểu hỏng gây ra gì

**Dự đoán:** chuyện gì xảy ra nếu readiness probe hỏng? Còn liveness hỏng?

```bash
# Làm hỏng readiness bằng cách gỡ Postgres đi.
kubectl -n data scale cluster kalapa-db --replicas=0 2>/dev/null || \
  kubectl -n data patch cluster kalapa-db --type merge -p '{"spec":{"instances":0}}'

watch kubectl -n kalapa get pods,endpoints
```

Quan sát: pod chuyển sang `0/1 Running` — **không bị restart**, nhưng bị gỡ
khỏi Service endpoints. Traffic ngừng tới nó. Đưa Postgres trở lại thì nó tự
quay vào.

Giờ so sánh:

```bash
kubectl -n data patch cluster kalapa-db --type merge -p '{"spec":{"instances":1}}'
# Chờ hồi phục, rồi sửa chart để livenessProbe trỏ tới /healthz/ready
# thay vì /healthz/live, và deploy lại.
# Gỡ Postgres đi lần nữa.
```

Quan sát: giờ mọi pod **restart**, liên tục, và cột `RESTARTS` tăng dần. Một
cú chập database 30 giây đã biến thành CrashLoopBackOff. Đây là sự cố tự gây
ra phổ biến nhất trong Kubernetes.

**Viết lại:** vì sao liveness không được có phụ thuộc bên ngoài.

---

## Lab 2 — Deploy không gián đoạn, và mấy con 502 khi thiếu preStop

**Dự đoán:** với `maxUnavailable: 0`, một lần rolling restart có thật sự không
mất request nào không?

```bash
# Terminal 1
./scripts/load.sh 120 20
# Terminal 2
kubectl -n kalapa rollout restart deploy/gateway
```

Kỳ vọng: không có lỗi nào.

Giờ bỏ khoảng dừng. Trong `deploy/charts/go-service/values.yaml` đặt
`preStopSleepSeconds: 0`, deploy lại, rồi lặp lại.

Kỳ vọng: vài lỗi, lần nào cũng có, đúng vào lúc pod cũ biến mất. Việc gỡ
endpoint và việc gửi SIGTERM diễn ra *song song*, nên không có khoảng dừng thì
process ngừng nhận trong khi Ingress vẫn đang định tuyến tới nó.

**Viết lại:** phép tính
`preStop + shutdownTimeout < terminationGracePeriodSeconds`, và mỗi đầu hỏng
cái gì nếu nó không đúng.

---

## Lab 3 — Số partition Kafka là trần của mức song song phía consumer

**Dự đoán:** `scoring` được scale lên 3 replica trên một topic 1 partition. Bao
nhiêu cái thực sự làm việc?

```bash
kubectl -n kalapa scale deploy/scoring --replicas=3
kubectl -n kalapa logs -l app.kubernetes.io/name=scoring --prefix --tail=20 | grep -i 'partition\|rebalance'
```

Đáp án: một. Hai cái còn lại vào consumer group, không được giao partition nào,
và ngồi không — tốn RAM mà chẳng làm gì.

Giờ tăng số partition:

```bash
kubectl -n kafka patch kafkatopic kyc.application.submitted \
  --type merge -p '{"spec":{"partitions":3}}'
./scripts/load.sh 60 10
kubectl -n kalapa logs -l app.kubernetes.io/name=scoring --prefix --tail=50 | grep -i partition
```

Giờ cả ba đều làm việc. Lưu ý số partition **chỉ tăng được, không bao giờ
giảm**, và việc tăng sẽ phân bố lại key giữa các partition — nên thứ tự theo
key bị phá với mọi key bị dời chỗ.

**Viết lại:** vì sao scale consumer vượt số partition là vô nghĩa, và việc
tăng số partition phải trả giá gì.

---

## Lab 4 — HPA, và vì sao utilisation tính theo request

**Dự đoán:** `requests.cpu: 50m`, `limits.cpu: 300m`, mục tiêu 70%. Nó scale
ra ở mức CPU thật là bao nhiêu?

```bash
# Trong deploy/charts/gateway/values.yaml:
#   go-service.autoscaling.enabled: true
#   go-service.autoscaling.maxReplicas: 3
git commit -am "lab4: bật HPA cho gateway" && git push     # Argo CD đồng bộ

kubectl -n kalapa get hpa -w        # terminal 1
./scripts/load.sh 300 40            # terminal 2
```

Đáp án: 35m — 70% của *request*, tức 12% của limit. Nó scale ra khi mới dùng
chưa tới một phần tám trần của mình.

Rồi xem nó thu lại. Mất 5 phút (`scaleDownStabilizationSeconds: 300`) một cách
có chủ ý: thu hẹp ngay sau một đợt tăng chỉ tái tạo đợt tăng đó trên số pod
còn lại.

Giờ thử cái bug:

```bash
# Đặt CẢ autoscaling.enabled: true LẪN replicaCount: 1, rồi push.
kubectl -n kalapa get deploy gateway -w
```

`selfHeal` của Argo CD kéo số replica về mỗi hai phút, xoá sạch quyết định của
HPA.

**Viết lại:** vì sao phải bỏ `replicas` khi HPA sở hữu nó, và vì sao đặt
request quá thấp làm HPA nhạy quá mức.

---

## Lab 5 — Bật xác thực

Keycloak đã được deploy kèm realm, client và một user demo, nhưng
`auth.enabled: false`.

```bash
# 1. Lấy token.
TOKEN=$(curl -s -X POST \
  http://identity.kalapa.local/realms/kalapa/protocol/openid-connect/token \
  -d 'client_id=kalapa-api' -d 'grant_type=password' \
  -d 'username=officer' -d 'password=officer' | jq -r .access_token)

# 2. Xem nó. Để ý `iss` và `aud`.
echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq

# 3. Bật xác thực: trong deploy/charts/kalapa-config/values.yaml,
#    config.auth.enabled: true. Commit và push.

# 4. Không token -> 401
curl -i http://api.kalapa.local/api/kyc/applications

# 5. Có token -> 200
curl -s -H "Authorization: Bearer $TOKEN" http://api.kalapa.local/api/kyc/applications | jq
```

Giờ cố tình làm hỏng. Đổi `config.auth.issuerUrl` thành bất cứ thứ gì khác rồi
push. Mọi request thành 401 kèm `issuer mismatch` trong log của gateway. Lỗi
đó chính là thứ bạn nhận được, ở mọi stack, khi `KC_HOSTNAME` của Keycloak và
issuer mà bên verify kỳ vọng không khớp nhau.

**Viết lại:** vì sao cách sửa là cấu hình hostname cho đúng, chứ không phải
nới lỏng việc kiểm tra issuer.

---

## Lab 6 — Transactional outbox

`kyc` commit bản ghi, rồi mới phát sự kiện. Nếu việc phát hỏng thì bản ghi tồn
tại mà sự kiện chưa từng được gửi — một lần ghi kép, và nó nằm trong code một
cách có chủ ý.

Chứng minh trước đã:

```bash
kubectl -n kafka scale statefulset kalapa-combined --replicas=0
curl -X POST http://api.kalapa.local/api/kyc/applications \
  -H 'Content-Type: application/json' \
  -d '{"national_id":"079999999999","full_name":"Outbox Demo"}'
# -> 202 Accepted, kèm cảnh báo. Bản ghi đã lưu, không gì được phát đi.
kubectl -n kafka scale statefulset kalapa-combined --replicas=1
# Sự kiện đã mất. Nó sẽ không bao giờ được chấm điểm.
```

Xây cách sửa:

1. Thêm bảng `outbox`: `id, topic, key, payload, created_at, published_at`.
2. Trong `kyc.submit`, INSERT hồ sơ **và** bản ghi outbox trong **một
   transaction**.
3. Thêm một goroutine quét `WHERE published_at IS NULL`, phát đi, rồi đóng dấu
   `published_at`.
4. Lặp lại bài test. Sự kiện phải tới nơi khi Kafka quay lại.

**Viết lại:** vì sao giờ nó là at-least-once chứ không phải exactly-once, và
điều gì làm cho handler của `scoring` an toàn khi bị gửi lại. (Gợi ý: nó vốn
đã được viết với `ON CONFLICT DO UPDATE`.)

---

## Lab 7 — Sealed Secrets

Repo commit mật khẩu dev dạng chữ rõ. Hãy sửa cho đàng hoàng.

```bash
helm install sealed-secrets sealed-secrets \
  --repo https://bitnami-labs.github.io/sealed-secrets \
  -n kube-system
brew install kubeseal

kubectl -n kalapa create secret generic kalapa-postgres-credentials \
  --from-literal=POSTGRES_USERNAME=kalapa \
  --from-literal=POSTGRES_PASSWORD='mật khẩu thật' \
  --dry-run=client -o yaml \
  | kubeseal --format yaml > deploy/charts/kalapa-config/templates/sealed-secret.yaml

# Xoá template Secret chữ rõ, commit, push.
```

Rồi trả lời: chuyện gì xảy ra khi bạn xoá cluster và dựng lại? (Controller
sinh một cặp khoá mới; các sealed secret của bạn không giải mã được nữa. Hãy
tìm xem khoá nằm ở đâu và sao lưu nó thế nào — đó mới là bài học thật.)

Sau đó, viết lại: khi nào bạn sẽ chọn External Secrets thay thế.

---

## Lab 8 — Cảnh báo

Alertmanager bị tắt để tiết kiệm ~80 MB. Bật nó lên và viết một luật thực sự
có thể đã bắt được điều gì đó.

```yaml
# deploy/manifests/alerts.yaml — một PrometheusRule
groups:
  - name: kalapa
    rules:
      - alert: HighErrorRate
        expr: |
          sum by (service) (rate(http_server_requests_total{status=~"5.."}[5m]))
            / sum by (service) (rate(http_server_requests_total[5m])) > 0.05
        for: 5m
      - alert: PodNearMemoryLimit
        expr: |
          container_memory_working_set_bytes{namespace="kalapa"}
            / kube_pod_container_resource_limits{resource="memory"} > 0.9
        for: 10m
      - alert: ScoringNotConsuming
        expr: |
          rate(kalapa_events_total{direction="produce"}[10m]) > 0
            and rate(kalapa_events_total{direction="consume"}[10m]) == 0
        for: 5m
```

Rồi kích hoạt từng cái. Cái thứ ba là cái thú vị: scale `scoring` xuống 0 rồi
sinh tải.

**Viết lại:** vì sao `for:` quan trọng, và một luật không có nó sẽ làm gì khi
hệ thống chập 30 giây.

---

## Lab 9 — NetworkPolicy, thực sự được thực thi

CNI mặc định của minikube bỏ qua NetworkPolicy. Hãy chứng minh điều đó rồi sửa.

```bash
# Trước: lệnh này thành công, nghĩa là không có gì được thực thi.
kubectl -n kalapa run probe --rm -it --image=busybox --restart=Never -- \
  wget -qO- --timeout=3 http://kyc/kyc/applications

minikube delete -p kalapa
CNI=calico make cluster && make up-local

# Bật policy: networkPolicy.enabled: true trong values của từng service.
# Khi đó cùng lệnh probe đó phải TIMEOUT, trong khi gateway vẫn chạy.
```

Giờ cố tình phá: xoá luật egress cho DNS khỏi
`deploy/charts/go-service/templates/networkpolicy.yaml` rồi deploy lại. Mọi
lời gọi ra ngoài đều hỏng — và log thì đổ lỗi cho service đích, không phải DNS.

**Viết lại:** vì sao NetworkPolicy đầu tiên bạn viết lại là cái nguy hiểm.

---

## Lab 10 — Service mesh

Thêm Linkerd (nhẹ hơn Istio; ~400 MB) và có mTLS giữa các service mà không
phải chạm vào ứng dụng.

```bash
curl -sL https://run.linkerd.io/install | sh
linkerd install --crds | kubectl apply -f -
linkerd install | kubectl apply -f -
kubectl annotate ns kalapa linkerd.io/inject=enabled
kubectl -n kalapa rollout restart deploy
linkerd viz install | kubectl apply -f -
linkerd viz stat deploy -n kalapa
```

Rồi so sánh: `linkerd viz stat` với dashboard RED trên Grafana. Chúng đo cùng
một thứ từ hai chỗ khác nhau.

**Viết lại:** mesh cho bạn thứ gì mà chart đã làm rồi (retry? timeout? mTLS?
observability?) và nó tốn bao nhiêu RAM. Rồi quyết định bạn có chạy nó không.

---

## Lab 11 — Change data capture với Debezium

YAS dùng Debezium để stream thay đổi từ Postgres vào Kafka. Hãy dựng nó ở đây.

```bash
# wal_level: logical đã được bật sẵn trên CNPG cluster.
kubectl -n data exec kalapa-db-1 -- psql -U kalapa -d kalapa \
  -c "ALTER TABLE kyc_applications REPLICA IDENTITY FULL;"

# Rồi một cặp KafkaConnect + KafkaConnector (Strimzi) với
# connector Debezium Postgres.
```

So sánh hai cách tiếp cận: ứng dụng tự phát sự kiện của nó (thứ `kyc` đang
làm) so với CDC đọc WAL.

**Viết lại:** cái nào giải quyết được vấn đề ghi kép ở lab 6, và CDC khiến bạn
phải trả giá gì về mặt ràng buộc. (CDC ràng buộc consumer vào schema bảng của
bạn. Outbox pattern cộng với CDC chỉ trên *bảng outbox* là cách tổng hợp
thường thấy.)

---

## Lab 12 — Phá nó, rồi tìm ra nó chỉ bằng telemetry

Nhờ người khác (hoặc một script) gài một trong các lỗi sau, rồi chẩn đoán chỉ
bằng Grafana — không `kubectl`, không đọc diff.

1. `limits.memory: 24Mi` trên `kyc` → vòng lặp OOMKill
2. `limits.cpu: 10m` trên gateway → throttle, latency tăng, không có lỗi
3. `maxConns: 1` trên pool database → xếp hàng khi có tải
4. trỏ `services.kyc` sang một hostname sai → 502 từ gateway
5. xoá topic Kafka → phát sự kiện hỏng, trả 202 thay vì 201
6. `sampleRatio: 0.0` → trace biến mất trong khi metric và log vẫn bình thường

Với mỗi cái: tín hiệu nào động đậy trước, panel nào cho thấy, và mất bao lâu
bạn gọi được tên nguyên nhân.

**Viết lại:** cái nào trong sáu cái bạn **không** chẩn đoán được chỉ bằng
telemetry, và bạn sẽ thêm gì vào dashboard để lần sau làm được. Chính khoảng
trống đó mới là kết quả thật của bài lab này.
