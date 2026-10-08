# 03 — Observability

Ba tín hiệu, và các liên kết giữa chúng. Chính các liên kết mới biến ba công
cụ đã cài thành một hệ thống; thiếu chúng thì bạn chỉ có Prometheus, Loki và
Tempo tình cờ nằm chung một cluster, dùng riêng rẽ — và phần lớn các stack
dừng ở đúng chỗ đó.

---

## Hình dạng tổng thể

```
                     ┌──────────────────────────────────────┐
  Service Go         │                                      │
    traces  ──OTLP──►│  OTel Collector  ──► Tempo           │
    metrics ◄─scrape─┤       │           ──► Prometheus ◄───┼── metrics-generator
    logs ───stdout───┼───► Alloy ──► Loki                   │   của Tempo
                     │                      │               │
                     │            Grafana ──┴── cả ba       │
                     └──────────────────────────────────────┘
```

**Trace được đẩy, metric được kéo.** Không phải ngẫu nhiên: một trace là sự
kiện xảy ra một lần và phải rời khỏi process trước khi process thoát, còn một
metric là giá trị hiện tại, đẩy đi vào lúc không ai hỏi thì vô nghĩa. Hệ quả
là trace cuối cùng của một pod đang chết vẫn tới nơi, còn metric cuối cùng thì
không — đó là lý do `cmd/*/main.go` flush span processor khi tắt:

```go
defer func() {
    flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = shutdownTracing(flushCtx)   // thiếu dòng này thì span của những giây
}()                                 // cuối trong một đợt rollout hỏng bị vứt đi
```

**Log đi ra stdout và không đi đâu khác.** Ứng dụng không biết Loki tồn tại.
Đó là cố ý: stdout là nơi duy nhất vẫn hoạt động khi process đang chết, khi
mạng đứt, hoặc khi chính hệ thống log là thứ bị hỏng.

---

## 1. Metrics

### Prometheus tìm thấy một target bằng cách nào

```
ServiceMonitor  ──chọn──►  Service  ──chọn──►  Pods
      ▲
      └── Prometheus chỉ nhận nó nếu label khớp với
          serviceMonitorSelector của CR Prometheus
```

Mũi tên cuối cùng đó là chỗ metric âm thầm biến mất. Chart đặt:

```yaml
# deploy/charts/go-service/templates/servicemonitor.yaml
labels:
  release: {{ .Values.serviceMonitor.prometheusRelease }}   # phải bằng tên
                                                            # release của
                                                            # kube-prometheus-stack
```

và repo này *cũng* nới lỏng selector để cái label đó không bắt buộc:

```yaml
# deploy/argocd/platform/10-kube-prometheus-stack.yaml
serviceMonitorSelectorNilUsesHelmValues: false   # nhận mọi ServiceMonitor
```

Cả hai được trình bày có chủ đích. Đổi cái `false` kia về mặc định là bạn có
hành vi nghiêm ngặt, tức là thứ bạn sẽ gặp trong cluster thật.

Khi một target mất tích, debug theo thứ tự:

```bash
# 1. ServiceMonitor có tồn tại không?
kubectl -n kalapa get servicemonitor

# 2. Prometheus thực sự chịu nhận cái gì?
kubectl -n observability get prometheus -o yaml | grep -A5 serviceMonitorSelector

# 3. Nó có dựng được scrape job không?
kubectl -n observability port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090
open http://localhost:9090/targets

# 4. Bản thân endpoint có trả lời không?
kubectl -n kalapa port-forward deploy/kyc 9090:9090
curl -s localhost:9090/metrics | head
```

### Các service phơi ra những gì

`internal/platform/obs/metrics.go`, trên một **registry riêng** thay vì
`prometheus.DefaultRegisterer` — nhờ vậy `/metrics` chứa đúng thứ repo này
chọn công bố, chứ không phải thứ mà một dependency gián tiếp nào đó đã đăng ký
toàn cục.

| Metric | Loại | Dùng để |
|---|---|---|
| `http_server_requests_total{method,route,status}` | counter | tần suất, lỗi |
| `http_server_request_duration_seconds` | histogram | phân vị latency |
| `http_server_requests_in_flight` | gauge | mức đồng thời |
| `kalapa_events_total{topic,direction,outcome}` | counter | đường đi Kafka |
| `kalapa_build_info{service,version}` | gauge | bản build nào đang chạy |
| `go_*`, `process_*` | — | collector của runtime |

### Quy tắc cardinality

> Mỗi tổ hợp giá trị label khác nhau tạo ra một time series. Mỗi series tốn bộ
> nhớ của Prometheus, mãi mãi.

```go
// Label route là PATTERN đã đăng ký, không bao giờ là đường dẫn thô.
route := "unmatched"
if _, pattern := resolver.Handler(r); pattern != "" {
    route = pattern     // "GET /kyc/applications/{id}"
}
```

Dùng `r.URL.Path` sẽ tạo một series mới cho mỗi UUID hồ sơ. Một nghìn hồ sơ là
một nghìn series, nhân với mỗi method, nhân với mỗi status. Đó là cách một
Prometheus hết sạch RAM.

Cùng quy tắc đó áp cho label của Loki, và đó là lý do `trace_id` cố tình
**không** là một Loki label ở đây — xem §2.

### Exemplar

`enableFeatures: [exemplar-storage]` cho phép một bucket của histogram mang
theo một trace ID. Trong Grafana đó là một chấm trên đồ thị latency mà bạn bấm
vào để mở đúng cái trace chậm. Đây là liên kết hữu ích nhất trong cả stack: từ
"p99 nhảy lúc 14:05" tới đúng request đó, chỉ bằng một cú bấm.

---

## 2. Logs

### Đường ống

```
Go: slog JSON ──► stdout ──► /var/log/pods/*.log ──► Alloy ──► Loki
```

Cấu hình Alloy (`deploy/argocd/platform/14-alloy-logs.yaml`) gồm năm bước:
phát hiện pod trên node này → chuyển metadata thành label → đọc file → phân
tích → ghi.

### Quy tắc cardinality, lần nữa

Đây là quyết định quan trọng nhất trong một hệ Loki.

> Mỗi tổ hợp giá trị label khác nhau là một **stream riêng**, với mục index
> riêng và chunk riêng.

| Label | Số giá trị khác nhau | Đánh giá |
|---|---|---|
| `namespace` | ~7 | ổn |
| `pod` | vài chục | ổn |
| `container`, `app`, `level` | vài cái | ổn |
| `trace_id` | **một cho mỗi request** | thảm hoạ |

YAS đẩy `traceId` thành một Loki label:

```yaml
# yas: k8s/deploy/observability/opentelemetry/values.yaml
- action: insert
  key: loki.attribute.labels
  value: namespace,container,pod,level,traceId    # <- cái này
```

Trên cluster demo thì chạy được. Ở lưu lượng thật nó tạo một stream cho mỗi
request và index sụp đổ. Ở đây `trace_id` nằm **trong thân dòng log**:

```logql
{namespace="kalapa"} | json | trace_id="4bf92f3577b34da6a3ce929d0e0e4736"
```

Biểu thức lọc quét chunk thay vì quét index. Chậm hơn cho mỗi truy vấn, nhưng
sống được ở quy mô lớn. Và cấu hình `derivedFields` của Grafana vẫn biến giá
trị đó thành link bấm được, nên trải nghiệm người dùng y hệt.

### Vài truy vấn hữu ích

```logql
# Lỗi trên toàn namespace ứng dụng
{namespace="kalapa"} | json | level="error"

# Đường đi của một request xuyên mọi service
{namespace="kalapa"} | json | trace_id="<id>"

# Tỷ lệ lỗi theo service, dạng đồ thị
sum by (service) (rate({namespace="kalapa"} | json | level="error" [5m]))

# Consumer Kafka đang làm gì
{namespace="kalapa", app="scoring"} | json | line_format "{{.msg}} {{.application_id}}"
```

---

## 3. Traces

### Truyền context, và phần dễ bỏ sót

Qua HTTP thì miễn phí — `otelhttp` tự inject và extract `traceparent` ở cả
phía server lẫn phía client.

Qua Kafka thì **không có gì làm hộ bạn.** Không làm thủ công, `kyc` và
`scoring` sinh ra hai trace không liên quan và service graph mất cạnh nối giữa
chúng. `internal/platform/events/carrier.go` là cách sửa:

```go
// Producer: inject SAU khi span producer bắt đầu, để consumer trở thành con
// của span produce chứ không phải của HTTP handler.
otel.GetTextMapPropagator().Inject(ctx, &RecordCarrier{Record: rec})

// Consumer: extract TRƯỚC khi bắt đầu span consumer.
ctx = otel.GetTextMapPropagator().Extract(ctx, &RecordCarrier{Record: rec})
ctx, span := c.tracer.Start(ctx, "consume "+rec.Topic, ...)
```

`RecordCarrier` biến header của Kafka record thành giao diện `TextMapCarrier`
của OTel — `Get`, `Set`, `Keys`. Ba mươi dòng, và nó là khác biệt giữa một
trace kể được câu chuyện và hai trace không kể được gì.

`Set` ghi đè chứ không nối thêm, nên một record được phát lại sẽ không tích
luỹ nhiều header `traceparent` khiến `Extract` nhặt đại một cái.

```bash
# Nhìn header trên đường truyền:
make kafka-console
# Rồi vào Grafana > Explore > Tempo, tìm service.name = gateway.
# Trace phải đọc ra như sau:
#   gateway POST /api/kyc/applications
#     └─ kyc  POST /kyc/applications
#          └─ kyc.insert
#          └─ produce kyc.application.submitted
#               └─ consume kyc.application.submitted   (scoring)
#                    └─ scoring.handle
```

### Lấy mẫu

```yaml
tracing:
  sampleRatio: 1.0     # thiết lập cho lab; chính nó sẽ làm đầy đĩa của Tempo
```

```go
sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
```

`ParentBased` mới là nửa quan trọng: nó tôn trọng quyết định của chặng trước,
nên một trace được lấy mẫu hoặc bị bỏ nguyên cả cụm. Lấy mẫu độc lập ở mỗi
chặng sinh ra nửa trace, mà nửa trace còn tệ hơn không có trace — nó trông như
một service đã ngừng phản hồi.

### metrics-generator

Tempo suy ra metric RED và các cạnh của service graph từ span rồi remote-write
sang Prometheus. Cần hai thiết lập và chỉ một trong đó là hiển nhiên:

```yaml
metricsGenerator:
  enabled: true
  remoteWriteUrl: http://prometheus-...:9090/api/v1/write
overrides:
  defaults:
    metrics_generator:
      processors: [service-graphs, span-metrics]   # <- thiếu cái này thì
```                                                 #    không sinh ra gì cả

Đây là thứ làm đầy service map của Grafana. Nếu map trống, kiểm tra khối
`overrides` trước mọi thứ khác.

---

## 4. Các liên kết

Đây là phần thu hoạch. Cấu hình nằm ở
`deploy/manifests/grafana/datasources.yaml`.

| Từ | Tới | Cơ chế | Trả lời câu hỏi nào |
|---|---|---|---|
| metric | trace | exemplar | "cái đỉnh p99 vừa rồi *là* cái gì?" |
| trace | log | `tracesToLogsV2` | "service nói gì trong lúc xử lý nó?" |
| log | trace | `derivedFields` | "trong cùng request đó còn gì xảy ra nữa?" |
| trace | metric | `tracesToMetrics` | "cái span chậm này có chậm thường xuyên không?" |
| trace | service map | `serviceMap` | "cái gì gọi cái gì, và lỗi nằm ở đâu?" |

Liên kết log → trace, viết đầy đủ:

```yaml
derivedFields:
  - name: TraceID
    datasourceUid: tempo
    matcherRegex: '"trace_id":"(\w+)"'   # khớp định dạng log JSON của ta
    url: '${__value.raw}'
```

Đổi định dạng log là biểu thức này ngừng khớp — âm thầm. Link chỉ đơn giản
không còn hiện ra. Sự ràng buộc đó đáng để biết.

---

## 5. Một phiên debug thật

Triệu chứng: p99 của gateway nhảy vọt lúc 14:05.

```
1. Grafana > Kalapa overview > panel "Latency p50/p95/p99"
   Xác nhận cú nhảy, ghi lại khoảng thời gian.

2. Bấm vào chấm exemplar trên đường p99.
   -> Tempo mở đúng trace đó.

3. Đọc thác span. Span nào rộng?
   gateway 2.1s
     └─ kyc 2.0s
          └─ kyc.insert 1.9s          <- đây

4. Trên span đó, bấm "Logs for this span".
   -> Loki, đã lọc theo đúng pod và đúng khoảng thời gian.
   "slow query" ở mốc 1.9s.

5. Vẫn chưa chắc? Span mang theo k8s.pod.name từ Downward API.
   Lọc panel bộ nhớ trong Grafana theo pod đó — nó có đang bị throttle không?
```

Năm bước, không SSH, không grep. Mỗi bước nhảy là một trong các liên kết ở
trên. Bỏ bất kỳ liên kết nào thì chuỗi đứt ngay tại đó.

---

## 6. Collector mang lại gì

Mọi service đều có thể đẩy thẳng vào Tempo. Collector đứng giữa vì:

- **Một địa chỉ, mãi mãi.** Đổi Tempo sang Jaeger là một Application manifest,
  không phải deploy lại mọi service.
- **`k8sattributes`** làm giàu span bằng metadata pod/deployment/node mà ứng
  dụng không biết.
- **`memory_limiter` đứng đầu mọi pipeline.** Nó xả tải khi collector tiến gần
  limit của chính nó. Đặt ở sau, các processor phía trước đã cấp phát xong
  phần bộ nhớ gây OOM rồi.
- **`sending_queue` có giới hạn.** Tempo chết thì chỉ mất span, chứ không làm
  collector OOM.
- **Tail sampling, lọc, che dữ liệu nhạy cảm** đều trở thành thay đổi cấu hình
  ở đây thay vì thay đổi code ở ba service.

```bash
# Metric nội bộ của collector là nơi cần nhìn đầu tiên khi telemetry
# "biến mất" — một collector đang âm thầm vứt dữ liệu trông y hệt một
# collector không có traffic.
kubectl -n observability port-forward svc/opentelemetry-collector 8888:8888
curl -s localhost:8888/metrics | grep -E 'refused|dropped|failed'
```

---

## Danh sách tự kiểm

- [ ] Vì sao trace được đẩy còn metric được kéo
- [ ] Vì sao phải flush trace exporter khi tắt
- [ ] Label nào làm Prometheus chịu nhận một ServiceMonitor
- [ ] Vì sao label `route` là pattern chứ không phải đường dẫn
- [ ] Thiếu `RecordCarrier` thì hỏng gì, và triệu chứng trông ra sao
- [ ] Vì sao dùng lấy mẫu `ParentBased`, và lấy mẫu theo từng chặng sinh ra gì
- [ ] Service map của Tempo cần hai thiết lập nào
- [ ] Vì sao `memory_limiter` đứng đầu mọi pipeline của collector
