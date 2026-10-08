# 11 — Observability

> **Bài này trả lời:** Hệ thống chậm. Làm sao biết chậm ở đâu? Ba tín hiệu
> metric/log/trace khác nhau thế nào, và vì sao chỉ có đủ cả ba mới dùng được?
>
> **Cần xong bài:** [10](10-kafka-va-bat-dong-bo.md)

---

## 1. Lý thuyết

### 1.1 Monitoring khác Observability

**Monitoring** trả lời câu hỏi bạn **đã biết trước**: "CPU có quá 80% không?"
Bạn dựng dashboard cho những thứ bạn đoán sẽ hỏng.

**Observability** là khả năng trả lời câu hỏi bạn **chưa từng nghĩ tới**: "Vì
sao chỉ những request của khách hàng ở Đà Nẵng, qua gateway, vào buổi chiều,
lại chậm?"

Với một ứng dụng đơn khối, bạn SSH vào rồi `grep` log là xong. Với 20
microservice trên 50 pod liên tục bị tạo lại, cách đó không còn tồn tại.

### 1.2 Ba tín hiệu

| Tín hiệu | Trả lời | Chi phí | Dạng |
|---|---|---|---|
| **Metric** | "Bao nhiêu? Có bất thường không?" | Rẻ, kích thước cố định | Số theo thời gian |
| **Log** | "Chuyện gì đã xảy ra?" | Trung bình | Sự kiện rời rạc |
| **Trace** | "Một request đã đi qua những đâu, mất bao lâu ở từng chặng?" | Đắt | Cây các span |

Cách dùng điển hình: **metric cho biết có vấn đề** → **trace cho biết ở đâu**
→ **log cho biết vì sao**.

Thiếu một cái thì chuỗi đứt. Có metric mà không trace: biết chậm, không biết
chậm ở service nào. Có trace mà không log: biết span nào rộng, không biết nó
làm gì bên trong.

### 1.3 Đẩy hay kéo

| Tín hiệu | Cách đi | Vì sao |
|---|---|---|
| Metric | **Kéo** (Prometheus scrape) | Metric là giá trị hiện tại; đẩy vào lúc không ai hỏi thì vô nghĩa. Và scrape hỏng cũng là một tín hiệu ("target down") |
| Trace | **Đẩy** | Trace là sự kiện xảy ra một lần, phải rời khỏi process trước khi process chết |
| Log | **Đẩy**, qua stdout | stdout là nơi duy nhất còn hoạt động khi app đang chết hoặc mạng đứt |

Hệ quả quan trọng: **trace cuối cùng của một pod sắp chết vẫn tới nơi, metric
cuối cùng thì không.** Đó là lý do code phải flush trace exporter khi tắt.

### 1.4 Prometheus và cardinality

Prometheus lưu **time series**. Mỗi tổ hợp giá trị label khác nhau là **một
series riêng**, tốn bộ nhớ mãi mãi.

```
http_requests_total{method="GET", route="/kyc/applications/{id}", status="200"}
```

Là một series. Nhưng nếu `route` là đường dẫn **thô**:

```
http_requests_total{route="/kyc/applications/a3f2-..."}   series 1
http_requests_total{route="/kyc/applications/b7e1-..."}   series 2
...một series cho mỗi UUID
```

Đây gọi là **cardinality explosion**, và nó là cách giết một Prometheus nhanh
nhất.

> **Quy tắc:** label phải có tập giá trị **hữu hạn và nhỏ**. Không bao giờ đưa
> ID, UUID, email, hay trace ID vào label.

ServiceMonitor là cách khai báo "hãy scrape cái này", thay vì sửa
`prometheus.yml` bằng tay — việc bất khả thi khi pod đổi IP liên tục.

### 1.5 Loki và cardinality (lần nữa)

Loki cũng có label, và cũng có cùng quy tắc — nhưng hậu quả còn nặng hơn: mỗi
tổ hợp label là một **stream** riêng, với index và chunk riêng.

```
{namespace="kalapa", app="kyc", level="error"}    <- tốt, vài chục stream
{namespace="kalapa", trace_id="4bf92f35..."}      <- thảm hoạ, một stream/request
```

Loki được thiết kế để **index ít, quét nhiều**: bạn lọc bằng label để thu hẹp
tập stream, rồi quét nội dung bằng biểu thức. Nên `trace_id` nằm **trong thân
log**, và việc tìm theo nó là một phép lọc chứ không phải tra index.

### 1.6 Trace: span, context, và chặng Kafka

Một **trace** là cây các **span**. Mỗi span có: tên, thời điểm bắt đầu/kết
thúc, thuộc tính, và **ID của span cha**.

Để span ở service B trở thành con của span ở service A, B phải nhận được
`trace_id` và `span_id` của A. Việc chuyển đó gọi là **truyền context**.

- **Qua HTTP:** miễn phí. Thư viện tự thêm header `traceparent` khi gọi và tự
  đọc nó khi nhận.
- **Qua Kafka:** **không có gì làm hộ bạn.** Nếu producer không ghi
  `traceparent` vào header của record và consumer không đọc ra, bạn có **hai
  trace rời rạc** và biểu đồ service mất hẳn cạnh nối.

Đây là mảnh hay bị bỏ sót nhất trong một hệ tracing microservice.

### 1.7 Lấy mẫu

Trace đắt. Ở lưu lượng thật người ta chỉ giữ một phần.

Điều phải nhớ: **quyết định lấy mẫu phải nhất quán cho cả trace**. Nếu mỗi
chặng tự quyết độc lập, bạn nhận được **nửa trace** — và nửa trace còn tệ hơn
không có trace, vì nó trông như một service đã ngừng phản hồi.

Giải pháp: `ParentBased` — chặng đầu quyết định, các chặng sau tôn trọng.

### 1.8 Vì sao cần Collector ở giữa

Mọi service có thể đẩy thẳng vào Tempo. Collector đứng giữa vì:

- **Một địa chỉ, mãi mãi.** Đổi Tempo sang Jaeger là sửa một file, không phải
  deploy lại mọi service.
- **Làm giàu dữ liệu.** Gắn thêm tên pod/namespace/node mà ứng dụng không biết.
- **Chống quá tải.** `memory_limiter` xả bớt khi collector gần chạm trần.
- **Hàng đợi có giới hạn.** Tempo chết thì mất span, chứ không làm collector OOM.
- **Lấy mẫu, lọc, che dữ liệu nhạy cảm** đều thành thay đổi cấu hình.

### 1.9 Các liên kết — phần quan trọng nhất

Ba công cụ riêng rẽ không bằng một hệ thống. Thứ biến chúng thành hệ thống là
các liên kết:

| Từ | Tới | Cơ chế | Trả lời |
|---|---|---|---|
| metric | trace | **exemplar** | "cái đỉnh p99 vừa rồi là request nào?" |
| trace | log | `tracesToLogs` | "service nói gì lúc xử lý nó?" |
| log | trace | `derivedFields` | "trong request đó còn gì xảy ra nữa?" |
| trace | service map | metrics-generator | "cái gì gọi cái gì, lỗi ở đâu?" |

---

## 2. Trong repo này nằm đâu

### Metric: registry riêng

`internal/platform/obs/metrics.go:27`

```go
func NewMetrics(service, version string) *Metrics {
	reg := prometheus.NewRegistry()
```

Dùng registry riêng thay vì `prometheus.DefaultRegisterer` → `/metrics` chỉ
chứa thứ repo này chủ động công bố, không chứa thứ một dependency nào đó đã
đăng ký toàn cục.

### Metric: tránh nổ cardinality

`internal/platform/obs/metrics.go:91`

```go
// routeResolver được *http.ServeMux thoả mãn. Nó cho middleware biết PATTERN
// đã đăng ký TRƯỚC khi dispatch — cách duy nhất đáng tin để có label `route`
// hữu hạn.
type routeResolver interface {
	Handler(*http.Request) (http.Handler, string)
}
```

Và trong `Middleware` (dòng 98):

```go
		route := "unmatched"
		if resolver != nil {
			if _, pattern := resolver.Handler(r); pattern != "" {
				route = pattern      // "GET /kyc/applications/{id}"
			}
		}
```

> Nếu dùng `r.URL.Path` thì mỗi UUID là một series mới. Đây chính là mục 1.4
> hiện ra thành code.

### Metric: ServiceMonitor

`deploy/charts/go-service/templates/servicemonitor.yaml`

```yaml
metadata:
  labels:
    {{- include "go-service.labels" . | nindent 4 }}
    release: {{ .Values.serviceMonitor.prometheusRelease }}
spec:
  endpoints:
    - port: admin                       # port quản trị, không lộ ra Ingress
      path: {{ .Values.serviceMonitor.path }}
```

> Label `release:` là thứ quyết định Prometheus có **nhận** ServiceMonitor này
> hay không. Sai nhãn = metric biến mất im lặng. Chú thích trong file có cả
> lệnh để kiểm tra selector thật của Prometheus.

### Log: JSON có trace ID

`internal/platform/logging/logging.go` — handler bọc ngoài:

```go
func (h traceHandler) Handle(ctx context.Context, rec slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, rec)
}
```

Mọi dòng log gọi qua `ctx` tự động mang `trace_id`. Đó là nửa đầu của liên kết
log → trace.

### Log: thu thập bằng Alloy

`deploy/argocd/platform/14-alloy-logs.yaml`. Năm bước trong khối `configMap`:

1. `discovery.kubernetes` — tìm pod **trên node này**
2. `discovery.relabel` — biến metadata thành label
3. `local.file_match` — khai triển glob thành đường dẫn thật
4. `loki.source.file` + `loki.process` — đọc và phân tích
5. `loki.write` — đẩy vào Loki

Ba chú thích trong file đáng đọc kỹ, vì mỗi cái ghi lại một lỗi **đã thực sự
xảy ra** khi dựng repo:

- Thiếu `local.file_match` → mọi component báo healthy nhưng **không log nào
  tới nơi**
- Container runtime là docker → file chỉ là symlink, cần
  `mounts.dockercontainers: true`
- Sai stage giải mã → log tới nơi nhưng mất label `level`/`service`

Và phần chọn label:

```
                rule {
                  source_labels = ["__meta_kubernetes_namespace"]
                  target_label  = "namespace"
                }
```

Chỉ `namespace`, `pod`, `container`, `app`, `level`, `service`. Không có
`trace_id` — đúng mục 1.5.

### Trace: khởi tạo

`internal/platform/obs/tracing.go:38`

```go
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	if !o.Enabled || o.Endpoint == "" {
		return noop.NewTracerProvider().Tracer(o.ServiceName), ...
	}
```

> Propagator được cài **kể cả khi tracing tắt**: gateway vẫn chuyển tiếp
> `traceparent` nó nhận được, nên một chặng tắt trace thành một khoảng trống
> trong trace chứ không thành hai trace rời.
>
> Và khi tắt, nó trả về tracer **no-op** chứ không trả `nil` — nhờ vậy không
> chỗ gọi nào cần kiểm tra nil, và service vẫn chạy được khi xoá sạch namespace
> observability.

Lấy mẫu, dòng 91:

```go
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.SampleRatio))),
```

### Trace: qua Kafka — phần quan trọng nhất

`internal/platform/events/carrier.go` — toàn bộ file chỉ 40 dòng:

```go
type RecordCarrier struct{ Record *kgo.Record }

func (c *RecordCarrier) Get(key string) string { ... }
func (c *RecordCarrier) Set(key, value string) { ... }   // GHI ĐÈ, không nối thêm
func (c *RecordCarrier) Keys() []string { ... }
```

Nó biến header của Kafka record thành giao diện `TextMapCarrier` của OTel.

Producer, `internal/platform/events/events.go:80`:

```go
	otel.GetTextMapPropagator().Inject(ctx, &RecordCarrier{Record: rec})
```

Consumer, `internal/platform/events/events.go:186`:

```go
	ctx = otel.GetTextMapPropagator().Extract(ctx, &RecordCarrier{Record: rec})
	ctx, span := c.tracer.Start(ctx, "consume "+rec.Topic, ...)
```

Thứ tự quan trọng: **inject sau khi span producer bắt đầu**, **extract trước
khi span consumer bắt đầu**.

> `Set` ghi đè thay vì nối thêm — để một record được phát lại không tích luỹ
> nhiều `traceparent` khiến `Extract` nhặt đại một cái.

### Flush khi tắt

`cmd/kyc/main.go`:

```go
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(flushCtx)
	}()
```

Không có nó, span của những giây cuối cùng — **đúng những span bạn cần khi
điều tra một đợt rollout hỏng** — bị vứt đi.

### Collector

`deploy/argocd/platform/13-otel-collector.yaml`. Để ý thứ tự processor:

```yaml
          processors:
            memory_limiter:    # PHẢI đứng đầu
            k8sattributes:     # làm giàu
            batch:             # gom lô, đứng cuối
```

Chú thích giải thích vì sao `memory_limiter` phải đứng đầu: đặt sau thì các
processor phía trước đã cấp phát xong phần bộ nhớ gây OOM rồi.

### Các liên kết

`deploy/manifests/grafana/datasources.yaml`:

```yaml
          derivedFields:
            - name: TraceID
              datasourceUid: tempo
              matcherRegex: '"trace_id":"(\w+)"'     # khớp log JSON của ta
              url: '$${__value.raw}'
```

Và chiều ngược lại, `tracesToLogsV2` với `spanStartTimeShift` để thu hẹp cửa
sổ thời gian.

> Đổi định dạng log là biểu thức này ngừng khớp — **âm thầm**. Link chỉ đơn
> giản không hiện ra nữa. Sự ràng buộc đó đáng biết.

---

## 3. Thực hành

### 3.1 Xem metric thô

```bash
kubectl -n kalapa port-forward deploy/kyc 9090:9090 &
curl -s localhost:9090/metrics | grep -E "^(http_server|kalapa_)" | head -20
kill %1
```

### 3.2 Prometheus có thấy target không

```bash
kubectl -n observability port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090 &
open http://localhost:9090/targets
```

Hoặc bằng API:

```bash
curl -s 'http://localhost:9090/api/v1/query?query=kalapa_build_info' \
  | jq -r '.data.result[] | "\(.metric.service) \(.metric.version) \(.metric.pod)"'
```

Vài truy vấn PromQL đáng thử:

```bash
# Tần suất request theo service
curl -s --get 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=sum by (service) (rate(http_server_requests_total[5m]))' | jq -r '.data.result[]|"\(.metric.service) \(.value[1])"'

# Latency p95
curl -s --get 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket[5m])))' | jq -r '.data.result[0].value[1]'

# Bộ nhớ so với limit
curl -s --get 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=container_memory_working_set_bytes{namespace="kalapa",container!=""}' | jq -r '.data.result[]|"\(.metric.pod) \(.value[1])"'

kill %1
```

### 3.3 Tự tay làm Prometheus mất metric

```bash
# Phá label release của ServiceMonitor
kubectl -n kalapa label servicemonitor kyc release- --overwrite
```

Nếu `serviceMonitorSelectorNilUsesHelmValues` được đặt về mặc định thì target
sẽ biến mất. Trong repo này nó được nới lỏng, nên hãy đọc chú thích ở
`deploy/argocd/platform/10-kube-prometheus-stack.yaml` để hiểu hành vi nghiêm
ngặt bạn sẽ gặp ở cluster thật.

```bash
kubectl -n kalapa label servicemonitor kyc release=prometheus --overwrite
```

### 3.4 Truy vấn log

```bash
kubectl -n observability port-forward svc/loki-gateway 3100:80 &

# Có những label nào?
curl -s 'http://localhost:3100/loki/api/v1/labels' | jq -r '.data[]'

# Giá trị của label level
curl -s 'http://localhost:3100/loki/api/v1/label/level/values' | jq -r '.data[]'

# Log của namespace kalapa
curl -s --get 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={namespace="kalapa"} | json' \
  --data-urlencode 'limit=5' | jq -r '.data.result[].values[][1]' | head -5

kill %1
```

Để ý `trace_id` **có trong thân log** nhưng **không có trong danh sách label**.

### 3.5 Theo một trace xuyên ba service — bài quan trọng nhất

```bash
# Tạo một request
kubectl -n ingress-nginx port-forward svc/ingress-nginx-controller 18080:80 &
sleep 3
curl -s -X POST -H 'Host: api.kalapa.local' -H 'Content-Type: application/json' \
  -d '{"national_id":"079444555666","full_name":"Theo Trace"}' \
  http://localhost:18080/api/kyc/applications | jq -r .id
kill %1

# Chờ scoring xử lý
sleep 10

# Tìm trace trong Tempo
kubectl -n observability port-forward svc/tempo 3200:3200 &
sleep 3
curl -s 'http://localhost:3200/api/search/tag/service.name/values' | jq -r '.tagValues[]'
```

Giờ tìm một trace có cả chặng Kafka:

```bash
for TID in $(curl -s 'http://localhost:3200/api/search?tags=service.name%3Dkyc&limit=10' \
  | jq -r '.traces[].traceID'); do
  OUT=$(curl -s "http://localhost:3200/api/traces/$TID" | python3 -c "
import json,sys
d=json.load(sys.stdin); spans=[]
for b in d.get('batches',[]):
    svc=next((a['value']['stringValue'] for a in b['resource']['attributes'] if a['key']=='service.name'),'?')
    for ss in b.get('scopeSpans',[]):
        for s in ss.get('spans',[]):
            spans.append((svc,s['name'],s['spanId'],s.get('parentSpanId',''),int(s['startTimeUnixNano'])))
if not any('consume' in s[1] for s in spans): sys.exit(1)
spans.sort(key=lambda x:x[4]); byid={s[2]:s for s in spans}
def d_(s):
    n=0;p=s[3]
    while p and p in byid: n+=1;p=byid[p][3]
    return n
for s in spans: print(f\"  {'  '*d_(s)}[{s[0]}] {s[1]}\")
" 2>/dev/null)
  [ -n "$OUT" ] && { echo "TRACE $TID"; echo "$OUT"; break; }
done
kill %1
```

Bạn sẽ thấy cây:

```
  [gateway] POST /api/kyc/applications
    [gateway] gateway.proxy
      [gateway] HTTP POST
        [kyc] POST /kyc/applications
          [kyc] kyc.submit
            [kyc] kyc.insert
            [kyc] produce kyc.application.submitted
              [scoring] consume kyc.application.submitted
                [scoring] scoring.handle
```

**Hãy nhìn kỹ chỗ `consume` nằm dưới `produce`.** Đó là `RecordCarrier` đang
làm việc. Không có nó, `scoring` sẽ là một trace riêng biệt.

### 3.6 Dùng Grafana — cách bạn sẽ làm thật

```bash
make pf-grafana     # localhost:3000, admin/admin
```

1. **Explore → Tempo → Search**, `service.name = gateway`. Mở một trace.
2. Trên một span, bấm **"Logs for this span"** → nhảy sang Loki, đã lọc sẵn.
3. **Explore → Loki**, chạy `{namespace="kalapa"} | json | level="error"`. Mở
   một dòng, bấm vào link `TraceID` → nhảy sang Tempo.
4. **Dashboards → Kalapa — service overview**. Xem panel RED, panel bộ nhớ so
   với limit, panel sự kiện produce vs consume.

Năm phút làm bốn bước này đáng giá hơn nửa giờ đọc.

### 3.7 Chứng minh trace đứt khi thiếu truyền context

Không cần sửa code. Chỉ cần tắt tracing ở một service rồi quan sát:

```bash
kubectl -n kalapa set env deploy/scoring TRACING_ENABLED=false
kubectl -n kalapa rollout status deploy/scoring
./scripts/load.sh 20 3
```

Giờ tìm trace lại — `scoring` biến mất khỏi cây, để lại một khoảng trống sau
span `produce`. Đó chính là hình dạng bạn sẽ thấy nếu `RecordCarrier` bị thiếu
(chỉ khác là khi thiếu carrier, `scoring` tạo một trace **riêng** chứ không
biến mất).

```bash
kubectl -n kalapa set env deploy/scoring TRACING_ENABLED-
```

### 3.8 Metric nội bộ của Collector

```bash
kubectl -n observability port-forward svc/opentelemetry-collector 8888:8888 &
curl -s localhost:8888/metrics | grep -E "refused|dropped|failed|sent" | head
kill %1
```

> Một collector đang âm thầm vứt dữ liệu trông **y hệt** một collector không có
> traffic. Đây là chỗ đầu tiên phải nhìn khi telemetry "biến mất".

---

## 4. Tự kiểm

- [ ] Monitoring khác observability thế nào?
- [ ] Ba tín hiệu, mỗi cái trả lời câu gì? Chuỗi dùng điển hình?
- [ ] Vì sao metric kéo còn trace đẩy?
- [ ] Hệ quả của việc đó với một pod đang chết là gì?
- [ ] Cardinality explosion là gì? Cho một ví dụ cụ thể.
- [ ] Vì sao label `route` phải là pattern chứ không phải đường dẫn?
- [ ] Vì sao `trace_id` không được làm Loki label? Vậy tìm theo nó kiểu gì?
- [ ] Truyền context qua HTTP và qua Kafka khác nhau thế nào?
- [ ] Thiếu `RecordCarrier` thì trace trông ra sao?
- [ ] `ParentBased` giải quyết vấn đề gì?
- [ ] Collector ở giữa mang lại năm lợi ích nào?
- [ ] Bốn liên kết giữa các tín hiệu, mỗi cái trả lời câu hỏi gì?
- [ ] Vì sao phải flush trace exporter khi tắt?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Đưa ID vào label của metric | Nổ cardinality, Prometheus hết RAM |
| Đưa `trace_id` vào label của Loki | Một stream cho mỗi request, index sụp |
| Chỉ cài ba công cụ, không nối liên kết | Ba công cụ rời rạc, không phải observability |
| Quên flush trace khi tắt | Mất đúng những span cần nhất |
| Lấy mẫu độc lập ở mỗi chặng | Nửa trace, tệ hơn không có trace |
| Quên truyền context qua Kafka | Hai trace rời, service map mất cạnh |
| Sai label `release` trên ServiceMonitor | Metric biến mất im lặng |
| Log không có cấu trúc | Không truy vấn được, chỉ grep được |
| `sampleRatio: 1.0` ở production | Đầy đĩa Tempo |
| Không nhìn metric nội bộ của collector | Không biết dữ liệu đang bị vứt |

---

**Bài tiếp:** [12 — GitOps với Argo CD](12-gitops-argocd.md)
