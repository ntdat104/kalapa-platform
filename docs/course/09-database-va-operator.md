# 09 — Database và Operator

> **Bài này trả lời:** Chạy Postgres trong Kubernetes kiểu gì mà không mất dữ
> liệu? StatefulSet là gì? CRD và Operator — thứ làm Kubernetes mở rộng được —
> hoạt động ra sao?
>
> **Cần xong bài:** [08](08-helm.md)

---

## 1. Lý thuyết

### 1.1 Vì sao database khó hơn ứng dụng

Mọi thứ Deployment giả định đều sai với database:

| Deployment giả định | Database thì |
|---|---|
| Pod thay thế được cho nhau | Mỗi instance có dữ liệu riêng, không thay thế được |
| Thứ tự khởi động không quan trọng | Primary phải lên trước replica |
| Tên pod ngẫu nhiên là ổn | Replica cần địa chỉ ổn định của primary |
| Mất pod không mất gì | Mất pod = mất dữ liệu nếu không có volume bền |
| Scale là nhân bản | Scale database là thay đổi topology sao chép |

### 1.2 PersistentVolume: lưu dữ liệu sống lâu hơn pod

Ba khái niệm:

| Từ | Là gì | Ai tạo |
|---|---|---|
| **PersistentVolume (PV)** | Một mẩu lưu trữ thật (đĩa EBS, thư mục trên node…) | Admin, hoặc tự động |
| **PersistentVolumeClaim (PVC)** | Lời yêu cầu "tôi cần 10Gi, đọc-ghi" | Bạn, trong manifest |
| **StorageClass** | "Loại" lưu trữ, biết cách tự cấp PV | Nhà cung cấp cluster |

Luồng: bạn tạo PVC → StorageClass tự cấp một PV khớp → PVC được **gắn**
(bound) với PV → pod mount PVC.

Điểm quan trọng: **PVC sống lâu hơn pod.** Pod chết, pod mới mount lại đúng
PVC đó và thấy nguyên dữ liệu.

> Trên minikube, StorageClass mặc định là `standard`, cấp một thư mục trên
> node. Nghĩa là dữ liệu **không sống qua `minikube delete`**.

### 1.3 StatefulSet

StatefulSet là Deployment dành cho thứ có trạng thái. Khác biệt:

| | Deployment | StatefulSet |
|---|---|---|
| Tên pod | `kyc-7d9f-x2k4` ngẫu nhiên | `db-0`, `db-1`, `db-2` ổn định |
| Thứ tự khởi động | song song | tuần tự, 0 → 1 → 2 |
| Thứ tự xoá | song song | ngược lại, 2 → 1 → 0 |
| Lưu trữ | chung | **mỗi pod một PVC riêng**, giữ nguyên qua các lần tạo lại |
| DNS | chỉ Service | thêm `db-0.db-headless.ns` cho từng pod |

Tên ổn định cho phép cấu hình kiểu "replica, hãy sao chép từ `db-0`" — thứ
không viết được với Deployment.

### 1.4 Nhưng StatefulSet vẫn chưa đủ

StatefulSet cho bạn tên ổn định và đĩa riêng. Nó **không** biết:

- instance nào đang là primary
- primary chết thì thăng cấp replica nào
- sao lưu thế nào, khôi phục ra sao
- nâng cấp minor version mà không mất dịch vụ
- kết nối ứng dụng nên trỏ đi đâu sau khi failover

Những việc đó là **kiến thức vận hành Postgres**, và Kubernetes không có.

### 1.5 CRD và Operator — cách Kubernetes tự mở rộng

Đây là ý tưởng lớn thứ hai của khoá học, sau vòng lặp điều hoà.

**CRD (Custom Resource Definition)** cho phép bạn dạy apiserver một *loại
object mới*. Sau khi cài CRD, cluster hiểu `kind: Cluster` của CloudNativePG y
như nó hiểu `kind: Deployment`:

```bash
kubectl get clusters -n data          # loại object không có sẵn trong k8s
```

**Operator** là một controller chạy vòng lặp điều hoà cho loại object mới đó.

```
Bạn viết:         kind: Cluster, instances: 3
                        │
CNPG Operator:  ────────┘
   vòng lặp mãi mãi:
       mong muốn = 3 instance, 1 primary
       thực tế   = đọc trạng thái Postgres thật
       hành động = tạo pod, dựng sao chép, thăng cấp khi cần,
                   chuyển hướng Service, chạy sao lưu
```

Chính là mẫu bạn đã học ở bài 03, chỉ khác là **kiến thức vận hành của một
chuyên gia Postgres được đóng gói thành phần mềm**.

> Đây là lý do kiến trúc "mọi thứ đi qua apiserver" ở bài 03 quan trọng: thêm
> một operator không cần sửa bất cứ gì của Kubernetes. Nó chỉ là thêm một
> client nữa của cùng API.

### 1.6 CloudNativePG và ba Service của nó

Operator tạo ba Service cho mỗi Cluster:

| Service | Trỏ tới | Dùng khi |
|---|---|---|
| `<tên>-rw` | **primary hiện tại** | đọc và ghi — **ứng dụng dùng cái này** |
| `<tên>-ro` | chỉ các replica | truy vấn đọc nặng |
| `<tên>-r` | instance bất kỳ | đọc, không quan tâm độ trễ |

Khi primary chết, operator thăng cấp một replica rồi **chuyển hướng Service
`-rw`**. Ứng dụng không đổi cấu hình, không restart, không biết gì đã xảy ra.

Đây chính là ví dụ cụ thể nhất cho việc "đóng gói kiến thức vận hành".

---

## 2. Trong repo này nằm đâu

### Operator được cài thế nào

`deploy/argocd/platform/01-cnpg-operator.yaml`

```yaml
# (trích đoạn — phần metadata/apiVersion ở phía trên đã lược)
  annotations:
    # Operator đi trước custom resource mà nó điều hoà: apply một `Cluster`
    # khi CRD chưa tồn tại sẽ hỏng với "no matches for kind".
    argocd.argoproj.io/sync-wave: "-10"
spec:
  source:
    repoURL: https://cloudnative-pg.github.io/charts
    chart: cloudnative-pg
    targetRevision: 0.29.1
  syncPolicy:
    syncOptions:
      - ServerSideApply=true
```

> `ServerSideApply=true` cần cho CRD: chúng đủ lớn để vượt giới hạn 256 KB của
> annotation `last-applied-configuration` mà apply phía client dùng.

### Custom resource

`deploy/charts/postgres/templates/cluster.yaml:13`

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: {{ .Values.name }}
spec:
  instances: {{ .Values.instances }}
  imageName: {{ .Values.image }}

  postgresql:
    parameters:
      shared_buffers: {{ .Values.parameters.sharedBuffers | quote }}
      max_connections: {{ .Values.parameters.maxConnections | quote }}
      wal_level: logical
```

**Toàn bộ file chỉ có 70 dòng.** So với việc tự viết StatefulSet + Service +
ConfigMap + script failover + CronJob sao lưu — có lẽ 500 dòng và vẫn sai.

Đọc kỹ khối chú thích ở đầu file (dòng 1–12): nó giải thích ba Service mà
operator tạo ra.

### Thông tin đăng nhập — một chi tiết thiết kế đáng học

`deploy/charts/postgres/templates/credentials.yaml`

```yaml
kind: Secret
type: kubernetes.io/basic-auth
stringData:
  username: {{ .Values.owner | quote }}
  password: {{ .Values.password | quote }}
  POSTGRES_USERNAME: {{ .Values.owner | quote }}
  POSTGRES_PASSWORD: {{ .Values.password | quote }}
```

Bốn khoá cho hai giá trị. Vì sao: operator đòi loại `basic-auth` với khoá
`username`/`password`; còn pod ứng dụng muốn biến môi trường viết hoa. Nhét cả
hai vào **một** Secret thay vì tạo hai Secret — để chúng không bao giờ lệch
nhau.

> Một mật khẩu lệch giữa database và ứng dụng tạo ra lỗi xác thực trông y hệt
> lỗi mạng. Thiết kế này loại bỏ hẳn khả năng đó.

### Ứng dụng kết nối thế nào

`deploy/charts/kalapa-config/values.yaml`:

```yaml
  database:
    url: postgres://kalapa-db-rw.data:5432/kalapa?sslmode=disable
```

`-rw`, không phải tên pod. Đó là thứ làm failover vô hình.

Và code `internal/platform/db/db.go` — để ý khối cấu hình pool:

```go
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
```

> Tái tạo kết nối định kỳ là thứ ngăn một lần chuyển primary để lại pool bị
> ghim vào instance đã bị giáng cấp.

Và `MaxConns: 4` trong `deploy/charts/kalapa-config/values.yaml`. Postgres cấp phát bộ nhớ
cho **mỗi kết nối** ngay từ đầu, nên pool phình to là cách nhanh nhất để một
hệ "nhỏ" làm cạn `max_connections`.

### Migration

`internal/platform/db/db.go`, hàm `Migrate` — chạy mọi câu lệnh trong **một**
transaction. Và `internal/kyc/kyc.go`, biến `Schema`:

```go
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS kyc_applications (...)`,
	`CREATE INDEX IF NOT EXISTS kyc_applications_national_id_idx ...`,
}
```

Mọi câu đều `IF NOT EXISTS` vì migration chạy ở **mỗi lần pod khởi động**.

### Chờ database sẵn sàng

`internal/platform/db/db.go`, hàm `WaitReady`, gọi từ `cmd/kyc/main.go`:

```go
	if err := pool.WaitReady(ctx, 90*time.Second); err != nil {
		return err
	}
```

> Không có nó, pod khởi động trước Postgres sẽ chết ngay và đốt sạch ngân sách
> restart (`CrashLoopBackOff` có backoff tăng dần tới 5 phút). Chờ có kiểm
> soát thì tốt hơn nhiều — và `startupProbe` với ngân sách 90 giây ở bài 07
> chính là để che đúng khoảng này.

---

## 3. Thực hành

### 3.1 Nhìn CRD

```bash
kubectl get crd | grep cnpg
kubectl explain cluster.spec --api-version=postgresql.cnpg.io/v1 | head -30
```

`kubectl explain` hoạt động với CRD y như với object có sẵn — vì apiserver đã
được dạy về loại này.

### 3.2 Nhìn Cluster và những gì operator tạo ra

```bash
kubectl -n data get cluster kalapa-db
kubectl -n data get cluster kalapa-db -o yaml | yq '.status'
```

Khối `status` là nơi operator báo cáo. Để ý `instancesStatus`, `currentPrimary`.

```bash
# Operator đã tạo những gì?
kubectl -n data get all,pvc,secret
```

Bạn sẽ thấy một StatefulSet, ba Service (`-rw`, `-ro`, `-r`), một PVC, và vài
Secret chứng chỉ TLS mà bạn không hề viết.

### 3.3 Ba Service

```bash
kubectl -n data get svc
kubectl -n data get endpoints kalapa-db-rw
kubectl -n data get endpoints kalapa-db-ro     # rỗng — chỉ có 1 instance
```

### 3.4 Vào database

```bash
make psql
```

Trong psql:

```sql
\dt
SELECT status, count(*) FROM kyc_applications GROUP BY status;
\d kyc_applications
SELECT * FROM pg_stat_activity WHERE datname = 'kalapa';
\q
```

Câu cuối cho bạn thấy pool kết nối của ứng dụng — đếm xem có bao nhiêu, so với
`MaxConns: 4`.

### 3.5 Chứng minh dữ liệu sống lâu hơn pod

```bash
# Đếm số bản ghi
kubectl -n data exec kalapa-db-1 -- psql -U kalapa -d kalapa -tAc \
  'SELECT count(*) FROM kyc_applications;'

# Giết pod database
kubectl -n data delete pod kalapa-db-1
kubectl -n data get pods -w      # Ctrl-C khi thấy Running trở lại

# Đếm lại
kubectl -n data exec kalapa-db-1 -- psql -U kalapa -d kalapa -tAc \
  'SELECT count(*) FROM kyc_applications;'
```

Con số giữ nguyên. PVC vẫn còn, pod mới mount lại nó.

```bash
kubectl -n data get pvc      # vẫn Bound, không hề mất
```

### 3.6 Chứng kiến operator điều hoà

```bash
# Terminal 1 — log của operator
kubectl -n cnpg-system logs -f deploy/cnpg-operator-cloudnative-pg

# Terminal 2 — thay đổi mong muốn
kubectl -n data patch cluster kalapa-db --type merge \
  -p '{"spec":{"postgresql":{"parameters":{"max_connections":"60"}}}}'
```

Ở terminal 1 bạn sẽ thấy operator nhận ra thay đổi và lên kế hoạch áp dụng.
Đây là vòng lặp điều hoà, lần này cho một thứ Kubernetes không hề biết tới.

```bash
kubectl -n data exec kalapa-db-1 -- psql -U kalapa -tAc 'SHOW max_connections;'
```

Khôi phục bằng `helm upgrade` hoặc để Argo CD tự hoàn nguyên.

### 3.7 Status là nơi chứa câu trả lời

```bash
# Cố tình đặt sai một tham số
kubectl -n data patch cluster kalapa-db --type merge \
  -p '{"spec":{"imageName":"ghcr.io/cloudnative-pg/postgresql:999.0"}}'

sleep 20
kubectl -n data get cluster kalapa-db
kubectl -n data get cluster kalapa-db -o jsonpath='{.status.conditions}' | jq
kubectl -n data get pods     # pod mới sẽ ImagePullBackOff
```

Khôi phục:

```bash
kubectl -n data patch cluster kalapa-db --type merge \
  -p '{"spec":{"imageName":"ghcr.io/cloudnative-pg/postgresql:17.5"}}'
```

> **Bài học:** với custom resource, `kubectl get` chỉ cho bạn một dòng. Lý do
> thật luôn nằm trong `.status.conditions`. Thói quen này sẽ cứu bạn ở bài 10.

### 3.8 Xem migration chạy

```bash
kubectl -n kalapa logs deploy/kyc | grep -i schema
```

---

## 4. Tự kiểm

- [ ] Năm giả định của Deployment sai với database ở chỗ nào?
- [ ] PV, PVC, StorageClass khác nhau thế nào? Ai tạo cái nào?
- [ ] Vì sao PVC sống lâu hơn pod lại quan trọng?
- [ ] StatefulSet khác Deployment ở năm điểm nào?
- [ ] StatefulSet vẫn chưa làm được những gì?
- [ ] CRD là gì? Nó thay đổi điều gì ở apiserver?
- [ ] Operator là gì? Nó dùng lại mẫu nào đã học ở bài 03?
- [ ] CloudNativePG tạo ba Service nào? Ứng dụng dùng cái nào, vì sao?
- [ ] Vì sao Secret của database chứa bốn khoá cho hai giá trị?
- [ ] Vì sao `MaxConns: 4` chứ không phải 50?
- [ ] Vì sao mọi câu lệnh migration đều `IF NOT EXISTS`?
- [ ] Khi custom resource không Ready, phải nhìn vào đâu?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Chạy database bằng Deployment | Không có đĩa bền, không có thứ tự, không có failover |
| Ứng dụng trỏ vào tên pod | Failover là hỏng ngay |
| Apply custom resource trước khi cài CRD | `no matches for kind` — đây là lý do có sync wave |
| Pool kết nối quá to | Postgres cấp bộ nhớ cho mỗi kết nối; cạn `max_connections` |
| Migration không idempotent | Pod restart là hỏng |
| Quên `WaitReady` | CrashLoopBackOff với backoff tăng tới 5 phút |
| Chỉ nhìn `kubectl get` khi CR lỗi | Lý do thật nằm trong `.status.conditions` |
| Tưởng `minikube delete` không mất dữ liệu | StorageClass mặc định lưu trên node. Mất node là mất hết |

---

**Bài tiếp:** [10 — Kafka và bất đồng bộ](10-kafka-va-bat-dong-bo.md)
