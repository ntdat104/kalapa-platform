# 07 — Tài nguyên, probe, vòng đời

> **Bài này trả lời:** Vì sao pod bị giết? Vì sao app "khoẻ" mà không nhận
> traffic? Vì sao mỗi lần deploy lại rớt vài request?
>
> **Cần xong bài:** [06](06-configmap-va-secret.md)
>
> **Đây là bài quan trọng nhất phần B.** Phần lớn sự cố tự gây ra trong
> Kubernetes nằm gọn trong bài này.

---

## 1. Lý thuyết

### 1.1 requests và limits

```yaml
resources:
  requests: { cpu: 50m, memory: 48Mi }   # scheduler GIỮ CHỖ chừng này
  limits:   { cpu: 300m, memory: 96Mi }  # trần cgroup
```

- `50m` = 50 milli-CPU = 0,05 lõi
- `48Mi` = 48 mebibyte (1 Mi = 1.048.576 byte; `M` khác `Mi`)

**requests** dùng để xếp lịch. Scheduler cộng requests của mọi pod trên một
node và chỉ đặt thêm pod nếu còn chỗ. Nó **không** quan tâm pod thật sự dùng
bao nhiêu.

**limits** là trần cứng do nhân thực thi. Và hai loại tài nguyên hành xử hoàn
toàn khác nhau:

| Vượt | Chuyện gì xảy ra |
|---|---|
| **Bộ nhớ** | Nhân **giết** process ngay lập tức. Exit code 137. Không cảnh báo, không cứu được |
| **CPU** | Process bị **làm chậm** (throttle). Không bao giờ bị giết |

Khác biệt này quyết định cách bạn gỡ lỗi: pod chết đột ngột → nghi bộ nhớ;
latency tăng mà không có lỗi → nghi CPU throttle.

### 1.2 Ba lớp QoS

Kubernetes xếp pod vào ba lớp dựa trên requests/limits. Khi node hết RAM, nó
evict theo thứ tự:

| Lớp | Điều kiện | Bị evict |
|---|---|---|
| `Guaranteed` | requests == limits, cho **mọi** tài nguyên và **mọi** container | cuối cùng |
| `Burstable` | có requests, nhỏ hơn limits | ở giữa |
| `BestEffort` | **không khai báo gì** | **đầu tiên** |

> Không khai báo `resources` không phải là "không giới hạn". Nó là "ưu tiên
> thấp nhất, chết trước". Và pod đó cũng vô hình với HPA, vì HPA tính phần
> trăm *của request* mà không có request nào.

### 1.3 Ba loại probe

Kubernetes hỏi ba câu khác nhau. Trả lời cả ba bằng cùng một endpoint là
nguyên nhân phổ biến nhất của sự cố tự gây ra.

| Probe | Câu hỏi | Hỏng thì làm gì | Tuyệt đối không phụ thuộc |
|---|---|---|---|
| `startupProbe` | "khởi động xong chưa?" | chờ tiếp, hết hạn mới restart | thứ chậm mà nó không kiểm soát |
| `livenessProbe` | "có bị treo cứng không?" | **restart container** | bất kỳ phụ thuộc bên ngoài nào |
| `readinessProbe` | "nhận traffic được chưa?" | **gỡ khỏi Service endpoints** | không gì — chỗ của phụ thuộc là đây |

### 1.4 Vì sao liveness không được kiểm tra database

Đây là bài học đắt giá nhất của bài này.

Giả sử `livenessProbe` có ping Postgres, và Postgres chập 30 giây:

```
t=0     Postgres chập
t=10    MỌI pod của MỌI service trượt liveness lần 1
t=20    trượt lần 2
t=30    trượt lần 3 -> kubelet giết TOÀN BỘ
t=31    Postgres hồi phục
t=31    nhưng mọi pod đang khởi động lại từ đầu, nguội hoàn toàn
t=35    tất cả cùng lúc mở kết nối tới Postgres
t=36    Postgres sập vì bão kết nối
t=...   lặp lại
```

Một cú chập 30 giây đã thành sự cố 10 phút, **nguyên nhân hoàn toàn là cái
health check**.

Readiness kiểm tra Postgres thì lại đúng: pod bị gỡ khỏi load balancer, ngừng
nhận traffic, **không restart**, và tự quay lại khi database hồi. Không bão,
không khởi động nguội.

> **Quy tắc:** liveness trả lời "process còn sống không", readiness trả lời
> "tôi có phục vụ được không". Phụ thuộc bên ngoài **luôn luôn** thuộc về
> readiness.

### 1.5 Vì sao cần startupProbe

Không có nó, bạn cần một probe vừa chịu được lần khởi động đầu 90 giây (chạy
migration, lấy metadata Kafka) *vừa* phát hiện treo trong 30 giây. Hai yêu cầu
mâu thuẫn.

`startupProbe` tách chúng: nó được ngân sách rộng, và **liveness chưa bắt đầu
cho tới khi nó đạt**. Sau đó liveness chạy chặt cho cả phần đời còn lại.

### 1.6 Tắt mềm — năm thứ phải khớp nhau

Đây là chỗ phân chia giữa "máy tôi chạy được" và "deploy nào cũng rớt vài
request".

```
  t=0   kubelet quyết định chấm dứt pod
        │
        ├─► endpoints controller bắt đầu gỡ pod khỏi Service
        │   (BẤT ĐỒNG BỘ — kube-proxy và Ingress biết tin muộn hơn)
        │
        └─► preStop hook chạy            [chart: lifecycle.preStop.sleep = 5s]
            │   container VẪN ĐANG PHỤC VỤ ở đây — đó chính là mục đích
            │
  t=5   SIGTERM được gửi                 [Go: signal.NotifyContext]
        │
        ├─► readiness chuyển false        [httpx: s.ready.Store(false)]
        ├─► ngừng nhận kết nối mới, xả nốt request đang chạy  [20s]
        └─► trace exporter flush
        │
  t=25  process thoát sạch
  t=45  kubelet SẼ SIGKILL nếu còn sống   [terminationGracePeriodSeconds]
```

Phép tính phải đúng:

```
preStop (5) + shutdownTimeout (20) < terminationGracePeriodSeconds (45)
```

**Vì sao cần khoảng dừng preStop:** việc gỡ endpoint và việc gửi SIGTERM diễn
ra **song song**, không nối tiếp. Không có khoảng dừng, process ngừng nhận
trong khi Ingress vẫn đang gửi request tới → đúng mấy con 502 mà người ta thấy
ở mọi lần rolling update và thường đổ cho load balancer.

### 1.7 Phần riêng của Go trong container

Một process Go **không đọc cgroup**. Để mặc, nó thấy toàn bộ CPU và RAM của
*node* rồi tự cấu hình theo đó — trong khi bị nhốt trong hộp 300m/96Mi.

| Biến | Vì sao cần đặt |
|---|---|
| `GOMAXPROCS` | Số OS thread Go dùng để chạy goroutine. Mặc định = số lõi của node. Trên node 10 lõi với limit 0,3 lõi → 10 thread tranh nhau 0,3 lõi → thrash chuyển ngữ cảnh, latency trông như lỗi mạng |
| `GOMEMLIMIT` | Trần **mềm** cho GC. Khi heap tiến gần nó, GC chạy mạnh hơn thay vì để nhân OOM-kill. Phải đặt thấp hơn `limits.memory` ~25% vì stack goroutine và cấu trúc runtime nằm **ngoài** heap |

### 1.8 HPA

```yaml
autoscaling:
  targetCPUUtilizationPercentage: 70
```

Ba điều không hiển nhiên:

**Utilisation tính theo request, không theo limit.** Với `requests.cpu: 50m`
và mục tiêu 70%, HPA scale ra ở **35m** CPU thật — tức 12% của limit 300m. Đặt
request quá thấp làm HPA nhạy quá mức.

**Không được đặt `replicas` khi HPA bật.** Đặt cả hai thì mỗi lần `helm
upgrade` (hoặc mỗi lần Argo CD đồng bộ) sẽ kéo số replica về giá trị cứng, xoá
quyết định của HPA.

**Cần metrics-server.** Thiếu nó HPA hiện `<unknown>/70%` mãi mãi.

---

## 2. Trong repo này nằm đâu

### Tài nguyên

`deploy/charts/go-service/values.yaml:46`

```yaml
resources:
  requests:
    cpu: 50m
    memory: 48Mi
  limits:
    cpu: 300m
    memory: 96Mi
```

requests ≠ limits → lớp **Burstable**. (Khối chú thích ngay phía trên trong
file giải thích vì sao lab này chọn như vậy.)

Và `deploy/charts/go-service/values.yaml:57`:

```yaml
goMemLimit: "72MiB"      # 75% của limits.memory
```

### Biến môi trường của Go

`deploy/charts/go-service/templates/deployment.yaml:115-128`

```yaml
            - name: GOMAXPROCS
              valueFrom:
                resourceFieldRef:
                  resource: limits.cpu
                  divisor: "1"
            - name: GOMEMLIMIT
              value: {{ .Values.goMemLimit | quote }}
```

`resourceFieldRef` đọc thẳng từ `limits.cpu` của chính container, nên đổi limit
là `GOMAXPROCS` tự theo. `divisor: "1"` làm tròn lên: 300m → 1.

### Probe

`deploy/charts/go-service/values.yaml:69`

```yaml
probes:
  startup:
    periodSeconds: 2
    failureThreshold: 30      # 30 x 2s = 60s cho lần khởi động đầu
  liveness:
    periodSeconds: 10
    failureThreshold: 3       # 3 x 10s = 30s treo liên tục mới restart
  readiness:
    periodSeconds: 5
    failureThreshold: 3       # nhanh hơn liveness — gỡ khỏi LB rẻ và đảo ngược được
```

`kyc` và `scoring` nới `startup` lên 45 lần vì chúng chạy migration và chờ
Kafka — xem `deploy/charts/kyc/values.yaml` khối `probes:`.

Template dùng chúng ở `deployment.yaml:163-188`. Cả ba đều trỏ vào `port: admin`.

### Code đứng sau probe

`internal/platform/httpx/server.go:79` — liveness:

```go
	mux.HandleFunc("GET /healthz/live", func(w http.ResponseWriter, r *http.Request) {
		// Cố ý không phụ thuộc gì. Nếu liveness kiểm tra Postgres, một cú chập
		// database sẽ restart mọi pod cùng lúc...
		if s.live.Load() { ... }
	})
```

`internal/platform/httpx/server.go:90` — readiness, chạy qua các checker:

```go
	mux.HandleFunc("GET /healthz/ready", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() { ... 503 ... }
		for name, check := range s.readinessChecks {
			if err := check(ctx); err != nil { ... }
		}
	})
```

Và các checker được đăng ký ở `cmd/kyc/main.go`:

```go
	srv.AddReadinessCheck("postgres", pool.Ping)
	srv.AddReadinessCheck("kafka", producer.Ping)
```

> Để ý gateway (`cmd/gateway/main.go`) **không** đăng ký checker nào. Lý do
> được ghi ngay trong code: gateway là stateless, và làm nó unready khi kyc
> chết sẽ kéo sập cả cửa vào, kể cả các endpoint không cần kyc.

### Tắt mềm

`deploy/charts/go-service/values.yaml:66`

```yaml
terminationGracePeriodSeconds: 45
preStopSleepSeconds: 5
```

`deployment.yaml:189-207`

```yaml
          lifecycle:
            preStop:
              sleep:
                seconds: {{ .Values.preStopSleepSeconds }}
# ...
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
```

> `sleep:` là lifecycle action gốc của Kubernetes (GA từ v1.33). Cách cũ là
> `exec: ["sh","-c","sleep 5"]` — **không chạy được ở đây vì image distroless
> không có shell**, và nó sẽ thất bại âm thầm.

Phía Go, `internal/platform/httpx/server.go:124` (hàm `Run`) thực hiện trình
tự ở mục 1.6:

```go
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	...
	// Bước 1: báo không sẵn sàng ngay
	s.ready.Store(false)
	// Bước 2: ngừng nhận, xả nốt
	appSrv.Shutdown(shutdownCtx)
	// Bước 3: admin đóng sau cùng
	adminSrv.Shutdown(shutdownCtx)
```

Thứ tự đó có lý do: admin đóng sau để Prometheus kịp scrape lần cuối và
kubelet vẫn nhận được câu trả lời trung thực tới phút chót.

### HPA

`deploy/charts/go-service/values.yaml:164` (mặc định tắt) và `deploy/charts/go-service/templates/hpa.yaml`. Để ý dòng bảo vệ ở
`deploy/charts/go-service/templates/deployment.yaml:16`:

```yaml
{{- if not .Values.autoscaling.enabled }}
replicas: {{ .Values.replicaCount }}
{{- end }}
```

---

## 3. Thực hành

### 3.1 Xem lớp QoS

```bash
kubectl -n kalapa get pods -o custom-columns=NAME:.metadata.name,QOS:.status.qosClass
```

### 3.2 Xem GOMAXPROCS thật sự là bao nhiêu

```bash
kubectl -n kalapa port-forward deploy/kyc 9090:9090 &
curl -s localhost:9090/metrics | grep -E "^go_(sched_gomaxprocs|goroutines|memstats_heap_inuse)"
kill %1
```

`go_sched_gomaxprocs_threads` phải là **1**, không phải 10. Nếu nó là 10 thì
biến môi trường chưa có tác dụng.

### 3.3 Gây OOMKill có chủ ý — bài quan trọng

```bash
kubectl -n kalapa set resources deploy/kyc --limits=memory=20Mi
kubectl -n kalapa get pods -l app.kubernetes.io/name=kyc -w
```

Trong vòng vài giây bạn sẽ thấy `OOMKilled` rồi `CrashLoopBackOff`. Xem chi
tiết:

```bash
POD=$(kubectl -n kalapa get pod -l app.kubernetes.io/name=kyc -o name | head -1)
kubectl -n kalapa get $POD -o jsonpath='{.status.containerStatuses[0].lastState}' | jq
```

Bạn sẽ thấy `"reason": "OOMKilled"` và `"exitCode": 137`. **Hãy nhớ con số
137** — nó là 128 + 9, tức là "bị giết bằng tín hiệu 9 (SIGKILL)".

Khôi phục:

```bash
kubectl -n kalapa set resources deploy/kyc --limits=memory=96Mi
kubectl -n kalapa rollout status deploy/kyc
```

### 3.4 Chứng kiến khác biệt readiness vs liveness

```bash
# Gỡ Postgres đi
kubectl -n data patch cluster kalapa-db --type merge -p '{"spec":{"instances":0}}'

# Theo dõi
kubectl -n kalapa get pods,endpoints -l app.kubernetes.io/name=kyc -w
```

Quan sát kỹ: pod chuyển `0/1` nhưng cột `RESTARTS` **không tăng**, và
endpoints rỗng đi. Đó là readiness làm việc — pod bị cô lập chứ không bị giết.

Xem lý do cụ thể:

```bash
kubectl -n kalapa port-forward deploy/kyc 9090:9090 &
curl -s localhost:9090/healthz/ready | jq
curl -s localhost:9090/healthz/live | jq     # vẫn UP!
kill %1
```

Bạn sẽ thấy `postgres: "DOWN: ..."` nhưng `kafka: "UP"` và liveness vẫn `UP`.
**Đây chính xác là hành vi đúng.**

Khôi phục:

```bash
kubectl -n data patch cluster kalapa-db --type merge -p '{"spec":{"instances":1}}'
```

### 3.5 Deploy không gián đoạn — và chứng minh preStop là cần thiết

```bash
# Terminal 1 — sinh tải
./scripts/load.sh 90 10

# Terminal 2 — restart giữa chừng
kubectl -n kalapa rollout restart deploy/gateway
```

Kết quả phải là **0 failed**.

Giờ bỏ khoảng dừng:

```bash
kubectl -n kalapa patch deploy gateway --type json \
  -p '[{"op":"replace","path":"/spec/template/spec/containers/0/lifecycle/preStop/sleep/seconds","value":0}]'

# Lặp lại hai terminal ở trên
```

Lần này bạn sẽ thấy vài lỗi. Đó là cái giá của 5 giây.

Khôi phục:

```bash
kubectl -n kalapa patch deploy gateway --type json \
  -p '[{"op":"replace","path":"/spec/template/spec/containers/0/lifecycle/preStop/sleep/seconds","value":5}]'
```

### 3.6 Bật HPA và nhìn nó làm việc

```bash
kubectl -n kalapa autoscale deploy gateway --cpu-percent=70 --min=1 --max=3
kubectl -n kalapa get hpa -w        # terminal 1
./scripts/load.sh 300 40            # terminal 2
```

Để ý cột `TARGETS` — nó hiện `<phần trăm hiện tại>/70%`. Tính nhẩm: ở mức bao
nhiêu milli-CPU thì nó scale ra? (Đáp án ở mục 1.8.)

Rồi chờ 5 phút xem nó thu lại — chậm có chủ ý.

Dọn dẹp:

```bash
kubectl -n kalapa delete hpa gateway
```

### 3.7 CPU throttle trông như thế nào

```bash
kubectl -n kalapa set resources deploy/gateway --limits=cpu=10m
./scripts/load.sh 60 20
```

Quan sát: **không pod nào chết**, nhưng latency tăng vọt. Đây là lý do throttle
khó chẩn đoán hơn OOM nhiều — không có sự kiện nào, chỉ có chậm.

```bash
kubectl -n kalapa port-forward deploy/gateway 9090:9090 &
curl -s localhost:9090/metrics | grep http_server_request_duration_seconds_bucket | tail -5
kill %1
kubectl -n kalapa set resources deploy/gateway --limits=cpu=300m
```

---

## 4. Tự kiểm

- [ ] Vượt limit bộ nhớ và vượt limit CPU khác nhau thế nào?
- [ ] Exit code 137 nghĩa là gì?
- [ ] Ba lớp QoS, cái nào bị evict trước? `resources: {}` cho lớp nào?
- [ ] Ba loại probe, mỗi loại hỏng thì Kubernetes làm gì?
- [ ] Vì sao liveness không được kiểm tra database? Mô tả chuỗi sự cố.
- [ ] Vì sao cần startupProbe mà không chỉ nới liveness ra?
- [ ] Viết lại trình tự tắt mềm, năm bước.
- [ ] Vì sao preStop phải ngủ? Việc gì diễn ra song song với nó?
- [ ] Vì sao không dùng `exec: sleep` trong repo này?
- [ ] `GOMAXPROCS` không đặt thì chuyện gì xảy ra trên node 10 lõi?
- [ ] Vì sao `GOMEMLIMIT` thấp hơn `limits.memory`?
- [ ] HPA với request 50m, mục tiêu 70% — scale ra ở mức CPU nào?
- [ ] Vì sao không được đặt cả `replicas` lẫn HPA?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Không đặt `resources` | QoS BestEffort, chết trước, vô hình với HPA |
| Liveness kiểm tra database | Một cú chập DB thành sự cố toàn hệ thống |
| Readiness và liveness cùng endpoint | Mất khả năng phân biệt "bận" và "chết" |
| Không có startupProbe | Hoặc liveness quá lỏng (chậm phát hiện treo), hoặc quá chặt (giết pod đang khởi động) |
| `terminationGracePeriodSeconds` nhỏ hơn thời gian xả | SIGKILL giữa chừng, cắt ngang request |
| Quên `preStop` | Vài con 502 ở mọi lần deploy, đổ oan cho load balancer |
| `exec: sleep` trên image distroless | Thất bại âm thầm, không có shell |
| Quên `GOMAXPROCS`/`GOMEMLIMIT` | Thrash CPU và OOM, dù config Kubernetes trông đúng |
| Đặt cả `replicas` lẫn HPA | Mỗi lần đồng bộ xoá quyết định của HPA |
| Probe trỏ vào port nghiệp vụ | App quá tải → probe cũng chậm → bị giết dù chỉ đang bận |

---

**Bài tiếp:** [08 — Helm](08-helm.md)
