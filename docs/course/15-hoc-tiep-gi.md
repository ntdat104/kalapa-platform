# 15 — Học tiếp gì

> **Bài này trả lời:** Bạn vừa học xong 14 bài. Bản đồ phần còn lại của thế
> giới infra trông thế nào, và nên đi hướng nào trước?

---

## 1. Bạn đã có gì

Nhìn lại cho rõ, vì dễ đánh giá thấp:

| Nhóm | Nắm được |
|---|---|
| **Nền tảng** | Process, port, container vs VM, image/layer/registry, multi-stage build |
| **Kubernetes** | Khai báo vs mệnh lệnh, vòng lặp điều hoà, control plane, Pod/ReplicaSet/Deployment, label/selector, Service/DNS/Ingress, ConfigMap/Secret, resources/QoS, ba loại probe, tắt mềm, HPA/PDB |
| **Đóng gói** | Helm: template, values, dependency, chart gốc dùng chung |
| **Trạng thái** | PV/PVC/StorageClass, StatefulSet, CRD, Operator, CloudNativePG |
| **Bất đồng bộ** | Kafka, partition, consumer group, offset, at-least-once, idempotent, ghi kép |
| **Vận hành** | Ba tín hiệu, cardinality, truyền trace qua Kafka, các liên kết trong Grafana |
| **Phát hành** | GitOps, app-of-apps, sync wave, drift, CI/CD kiểu kéo |
| **Bảo mật** | Distroless, securityContext, PSA, RBAC, NetworkPolicy, quản lý secret, xác thực JWT |

Quan trọng hơn danh sách đó: bạn đã **tự tay phá hỏng** từng thứ và sửa lại.
Đó là phần không đọc mà có được.

## 2. Việc nên làm ngay

**Bước 1 — Đọc lại bộ tài liệu tham khảo.**
[docs/00 → docs/08](../README.md). Lúc này chúng sẽ đọc như tài liệu tra cứu
chứ không còn như tiếng nước ngoài. Bài
[01-yas-mapping](../01-yas-mapping.md) đặc biệt đáng đọc — nó đối chiếu repo
này với một hệ thống Spring Boot 16 service thật.

**Bước 2 — Làm 12 bài lab.**
[docs/07-labs.md](../07-labs.md). Chúng khó hơn phần thực hành trong giáo
trình: mỗi bài yêu cầu bạn **dự đoán trước** rồi mới chạy. Bốn bài bắt bạn xây
thứ mới (outbox, Sealed Secrets, alert, CDC).

**Bước 3 — Dựng lại từ đầu, không nhìn tài liệu.**

```bash
make down
make cluster && make up-local && make smoke
```

Chỗ nào phải mở tài liệu ra xem là chỗ bạn chưa thật sự nắm.

**Bước 4 — Thêm service thứ tư.**

Bài tập tổng hợp tốt nhất. Một service `notification` đọc cùng topic Kafka
bằng một consumer group **khác**, rồi ghi log "đã gửi thông báo". Cần:

- `cmd/notification/main.go` và `internal/notification/` (bạn tạo mới)
- `deploy/charts/notification/` với `Chart.yaml` + `values.yaml` (bài 08)
- một khối trong `serviceConfig:` của `kalapa-config` (bài 06)
- một mục trong bộ lọc đường dẫn của CI (bài 13)

ApplicationSet sẽ **tự nhận ra** chart mới — bạn không phải viết Application
nào (bài 12). Nếu điều đó xảy ra thật thì bạn đã hiểu đúng kiến trúc.

## 3. Bản đồ phần còn lại

### 3.1 Nên học tiếp ngay

| Chủ đề | Vì sao | Bắt đầu từ đâu |
|---|---|---|
| **Kustomize** | Lựa chọn thay thế Helm: vá YAML thay vì template. Argo CD hỗ trợ sẵn. Nhiều nơi dùng Helm cho chart bên thứ ba và Kustomize cho app nhà | `kubectl kustomize` có sẵn |
| **RBAC chuyên sâu** | Giáo trình chỉ chạm nhẹ. Thực tế bạn sẽ phải cấp quyền cho người và cho CI | `kubectl auth can-i`, Role/RoleBinding |
| **Nhiều môi trường** | dev/staging/prod. Thư mục theo môi trường + lớp values. Thăng hạng bằng PR | Thêm `environments/` vào repo này |
| **Alerting** | Thu thập metric mà không cảnh báo thì ai nhìn? | Lab 8, rồi PrometheusRule + Alertmanager |
| **Sao lưu và khôi phục** | Chưa thực sự có cho tới khi bạn đã **khôi phục** thành công | Velero; `barmanObjectStore` của CNPG |

### 3.2 Khi hệ thống lớn lên

| Chủ đề | Giải quyết | Chi phí |
|---|---|---|
| **Service mesh** (Linkerd, Istio) | mTLS, retry, timeout, circuit breaker, traffic splitting — không sửa code | ~400 MB (Linkerd) tới ~1 GB+ (Istio). Thêm một tầng phải gỡ lỗi |
| **Policy engine** (Kyverno, OPA) | Áp luật ở admission cho **mọi** thứ, kể cả chart bạn không viết | Một controller; luật viết sai chặn luôn deploy hợp lệ |
| **Progressive delivery** (Argo Rollouts, Flagger) | Canary, blue-green, tự rollback theo metric | Cần nhiều replica và mesh/ingress chia được traffic |
| **Autoscaling nâng cao** (KEDA) | Scale theo consumer lag, độ dài hàng đợi — không chỉ CPU | Hợp với `scoring` trong repo này |
| **Multi-cluster** | Cô lập theo vùng, theo môi trường, chịu lỗi | Argo CD `destinations` đã hỗ trợ sẵn |

> Thứ tự này có chủ ý. Service mesh hay bị thêm vào quá sớm. Trước khi thêm,
> hãy hỏi: nó cho tôi thứ gì mà chart đã làm rồi? (Timeout? HTTP client đã có.
> Retry? Có thể viết trong code. mTLS? Cái này thì mesh thắng.)

### 3.3 Nền tảng cần đào sâu

Đây là thứ phân biệt người "dùng được Kubernetes" với người "gỡ lỗi được
Kubernetes":

| Chủ đề | Vì sao quan trọng |
|---|---|
| **Mạng Linux** | iptables/eBPF, cách kube-proxy thực sự định tuyến, CNI làm gì |
| **cgroup v2** | Cách limit được thực thi; vì sao kế toán bộ nhớ hay gây bất ngờ |
| **etcd** | Giới hạn của nó định hình giới hạn của Kubernetes |
| **TLS và PKI** | Chứng chỉ ở khắp nơi; hiểu chuỗi tin cậy |
| **Postgres chuyên sâu** | Mức cô lập, vacuum, kế hoạch truy vấn, connection pool |

### 3.4 Nếu bạn đi về phía SRE

| Chủ đề | Nội dung |
|---|---|
| **SLO và error budget** | Định lượng "đủ tin cậy". Thay đổi cách ra quyết định |
| **Phân tích sự cố không đổ lỗi** | Sự cố là dữ liệu, không phải lỗi của ai |
| **Kỹ thuật hỗn loạn** | Tiêm lỗi có chủ ý. Lab 12 là bản thu nhỏ |
| **Lập kế hoạch dung lượng** | Từ "nó chạy" sang "nó chạy ở quy mô gấp 10" |

Sách: *Site Reliability Engineering* của Google (đọc miễn phí), và
*Designing Data-Intensive Applications* của Kleppmann — cuốn sau giải thích
đúng những vấn đề bài 09 và 10 chạm vào, ở mức sâu hơn nhiều.

## 4. Tự đánh giá

Bạn thực sự nắm được bao nhiêu? Thử trả lời không nhìn tài liệu:

**Mức cơ bản** — nên trả lời được hết:

- [ ] Giải thích khai báo vs mệnh lệnh, kèm ví dụ
- [ ] Vẽ lại đường đi của `kubectl apply`
- [ ] Vì sao liveness không được kiểm tra database
- [ ] Endpoints rỗng — hai nguyên nhân?
- [ ] Vì sao `:latest` phá vỡ GitOps
- [ ] Cardinality explosion là gì, cho ví dụ

**Mức trung cấp** — nên trả lời được phần lớn:

- [ ] Vẽ trình tự tắt mềm, năm bước, và phép tính phải đúng
- [ ] Vì sao `GOMAXPROCS` phải đặt tường minh trong container
- [ ] Mô tả vấn đề ghi kép và cách outbox sửa nó
- [ ] Trace đi qua Kafka bằng cách nào? Thiếu nó thì thấy gì?
- [ ] Vì sao PSA từ chối Pod mà không từ chối Deployment
- [ ] Sync wave giải quyết gì mà `sleep 60` không

**Mức nâng cao** — trả lời được là bạn đã đi khá xa:

- [ ] Khi nào `ignoreDifferences` là cần thiết, và nhận ra bằng triệu chứng gì
- [ ] Vì sao `memory_limiter` phải đứng đầu pipeline của collector
- [ ] Đánh đổi giữa Sealed Secrets và External Secrets
- [ ] Khi nào service mesh đáng giá, khi nào không
- [ ] Thiết kế chiến lược sao lưu cho hệ thống này — và cách kiểm chứng nó

## 5. Một lời khuyên cuối

Thứ bạn vừa học không phải "Kubernetes". Kubernetes chỉ là công cụ, và nó sẽ
được thay thế.

Thứ **không** bị thay thế là các ý tưởng bên dưới:

- **Vòng lặp điều hoà.** Mô tả cái bạn muốn, để máy móc làm cho thực tế khớp
  với nó. Bạn gặp nó ba lần trong giáo trình này — ReplicaSet, Operator, Argo
  CD — và bạn sẽ còn gặp nữa.
- **Hạ tầng là code.** Nếu nó không nằm trong Git thì nó không tồn tại.
- **Thiết kế để hỏng.** Pod sẽ chết, mạng sẽ đứt, database sẽ chập. Thiết kế
  nào giả định mọi thứ chạy tốt là thiết kế sẽ hỏng lúc 3 giờ sáng.
- **Quan sát được là một tính chất của thiết kế.** Nó phải được xây vào, không
  gắn thêm sau.
- **Ranh giới phải tường minh.** Namespace, AppProject, NetworkPolicy,
  securityContext — mỗi cái là một câu trả lời cho "ai được làm gì", viết ra
  thành văn bản thay vì nằm trong trí nhớ ai đó.

Và điều này, từ kinh nghiệm dựng chính repo này: **sáu lỗi thật đã được tìm ra
không phải bằng cách đọc tài liệu, mà bằng cách chạy nó và nhìn nó hỏng.** Tài
liệu nói cho bạn biết cái gì *nên* xảy ra. Cluster nói cho bạn biết cái gì
*đang* xảy ra. Khi hai cái khác nhau, cluster luôn đúng.

---

**Quay lại:** [Mục lục giáo trình](README.md) · [Tài liệu tham khảo](../README.md) · [12 bài lab](../07-labs.md)
