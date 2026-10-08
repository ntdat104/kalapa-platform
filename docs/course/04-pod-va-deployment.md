# 04 — Pod, ReplicaSet, Deployment

> **Bài này trả lời:** Đơn vị chạy nhỏ nhất của Kubernetes là gì? Vì sao không
> ai tạo Pod trực tiếp? Label và selector hoạt động ra sao, và vì sao chúng là
> thứ dễ làm hỏng nhất?
>
> **Cần xong bài:** [03](03-vi-sao-can-kubernetes.md)

---

## 1. Lý thuyết

### 1.1 Pod — đơn vị chạy nhỏ nhất

Kubernetes không chạy container. Nó chạy **Pod**, và Pod chứa một hoặc nhiều
container.

Các container trong cùng một Pod:

- **dùng chung không gian mạng** → gọi nhau qua `localhost`, và không được
  trùng port
- **dùng chung volume** nếu khai báo
- **luôn nằm cùng một node**
- **sống chết cùng nhau** — scale là scale cả Pod, không scale từng container

Vì sao lại cần lớp trừu tượng này thay vì chạy thẳng container? Vì có những
thứ *bắt buộc* phải dính liền nhau: một app và một sidecar đọc log của nó, một
app và một proxy đứng trước nó. Chúng phải cùng mạng, cùng vòng đời, cùng node.

> **Mặc định nên là một container cho mỗi Pod.** Chỉ thêm container thứ hai
> khi nó thật sự không thể sống tách rời. Trong repo này mọi service đều một
> container; các pod có 2–3 container đều là của bên thứ ba (Grafana + sidecar
> nạp dashboard, Alloy + config-reloader).

**Pod là thứ dùng rồi vứt.** Nó không bao giờ "hồi phục". Node chết thì pod
chết hẳn, và một pod *mới* với tên mới, IP mới được tạo ở nơi khác. Mọi thiết
kế phải chịu được điều này.

### 1.2 Vì sao không ai tạo Pod trực tiếp

Tạo Pod trần thì: nó chết là xong, không ai bật lại; muốn 3 bản thì copy YAML
3 lần; muốn đổi image thì phải tự xoá tự tạo, và có khoảng thời gian không có
pod nào.

Nên người ta xếp chồng ba lớp:

```
Deployment      "tôi muốn 3 bản của image v2, cập nhật cuốn chiếu"
    │ tạo và quản
    ▼
ReplicaSet      "tôi đảm bảo luôn có đúng 3 Pod khớp selector này"
    │ tạo và quản
    ▼
Pod  Pod  Pod   process thật sự đang chạy
```

Mỗi lớp là một vòng lặp điều hoà riêng. Deployment điều hoà các ReplicaSet;
ReplicaSet điều hoà các Pod.

### 1.3 Rolling update diễn ra thế nào

Khi bạn đổi image trong Deployment, nó **không** sửa ReplicaSet cũ. Nó tạo
một ReplicaSet **mới** rồi dịch chuyển dần:

```
RS cũ (v1): 3 pod        RS mới (v2): 0 pod
RS cũ (v1): 3 pod        RS mới (v2): 1 pod   <- tạo 1 (maxSurge)
                                                 chờ nó Ready
RS cũ (v1): 2 pod        RS mới (v2): 1 pod   <- giờ mới gỡ 1 cái cũ
RS cũ (v1): 2 pod        RS mới (v2): 2 pod
...
RS cũ (v1): 0 pod        RS mới (v2): 3 pod
```

ReplicaSet cũ **được giữ lại** với 0 pod. Đó chính là thứ làm cho
`kubectl rollout undo` hoạt động: quay lại chỉ là tăng số pod của RS cũ lên.

Hai tham số điều khiển nhịp độ:

| Tham số | Nghĩa |
|---|---|
| `maxSurge: 1` | Được phép vượt số replica mong muốn tối đa 1 pod |
| `maxUnavailable: 0` | Không bao giờ được tụt xuống dưới số replica mong muốn |

`maxUnavailable: 0` = không gián đoạn, đổi lại cần chỗ trống cho 1 pod thừa.
`maxUnavailable: 1` với `replicas: 1` = **chắc chắn có khoảng mất dịch vụ** ở
mọi lần deploy, vì pod duy nhất bị gỡ trước khi bản thay thế sẵn sàng.

### 1.4 Label và selector — cơ chế liên kết của toàn bộ Kubernetes

Trong Kubernetes **không có con trỏ**. ReplicaSet không giữ danh sách ID pod
của nó. Thay vào đó mọi liên kết đều qua label.

```yaml
# ReplicaSet nói: "pod của tôi là mọi pod có nhãn này"
selector:
  matchLabels:
    app.kubernetes.io/name: kyc
    app.kubernetes.io/instance: kyc

# Pod mang nhãn đó
metadata:
  labels:
    app.kubernetes.io/name: kyc
    app.kubernetes.io/instance: kyc
```

Service tìm pod cũng theo cách đó. NetworkPolicy cũng vậy. ServiceMonitor cũng
vậy. Hiểu label là hiểu cách mọi thứ nối với nhau.

> **Luật sắt:** `selector` của Deployment là **bất biến**. Muốn đổi thì phải
> xoá Deployment rồi tạo lại. Vì vậy selector chỉ được chứa những nhãn *không
> bao giờ đổi* — tên và instance. Nhãn `version` mà nằm trong selector thì mỗi
> lần nâng version là một lần phải xoá Deployment.

Đó là lý do chart trong repo này tách làm hai hàm: một hàm sinh nhãn đầy đủ
để truy vấn, một hàm sinh đúng tập nhãn tối thiểu cho selector.

### 1.5 Namespace

Namespace chia cluster thành các vùng tên riêng. Hai Service tên `kyc` ở hai
namespace khác nhau là hai thứ khác nhau.

```bash
kubectl get pods                 # namespace hiện tại (thường là default)
kubectl get pods -n kalapa       # namespace cụ thể
kubectl get pods -A              # tất cả
```

Namespace cho bạn: ranh giới xoá (`kubectl delete ns X` dọn sạch vùng đó),
ranh giới phân quyền (RBAC), ranh giới mạng (NetworkPolicy), và ranh giới bảo
mật (Pod Security — bài 14).

---

## 2. Trong repo này nằm đâu

File chính: **`deploy/charts/go-service/templates/deployment.yaml`**. Đây là
file quan trọng nhất của toàn bộ repo — mọi service đều sinh ra từ nó.

### Số bản sao (dòng 16–20)

```yaml
{{- if not .Values.autoscaling.enabled }}
replicas: {{ .Values.replicaCount }}
{{- end }}
```

Trường này **biến mất hoàn toàn** khi HPA được bật. Lý do ở bài 07, nhưng tóm
tắt: đặt cả hai thì Helm và HPA giành nhau, và Helm luôn thắng ở mỗi lần đồng
bộ.

### Chiến lược cập nhật (dòng 22–28)

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: {{ .Values.strategy.maxUnavailable }}
    maxSurge: {{ .Values.strategy.maxSurge }}
```

Giá trị ở `deploy/charts/go-service/values.yaml:59`:

```yaml
strategy:
  maxUnavailable: 0
  maxSurge: 1
```

### Selector (dòng 30–32)

```yaml
selector:
  matchLabels:
    {{- include "go-service.selectorLabels" . | nindent 6 }}
```

Hàm `go-service.selectorLabels` nằm trong
`deploy/charts/go-service/templates/_helpers.tpl`. Mở file đó và so sánh nó
với hàm `go-service.labels` ngay phía trên:

- `labels` có cả `helm.sh/chart`, `app.kubernetes.io/version`,
  `app.kubernetes.io/managed-by` — những thứ **đổi theo từng lần release**
- `selectorLabels` chỉ có `name` và `instance` — những thứ **không bao giờ đổi**

> Đây chính là cách chart tránh bẫy "selector bất biến" ở mục 1.4. Nếu
> `version` lọt vào selector thì lần nâng chart đầu tiên sẽ hỏng với
> `field is immutable`.

### Giữ lại bao nhiêu bản cũ (dòng 20)

```yaml
revisionHistoryLimit: {{ .Values.revisionHistoryLimit }}   # = 3
```

Mặc định của Kubernetes là 10, nghĩa là mười ReplicaSet chết cho mỗi service
nằm mãi trong etcd.

### Namespace

`deploy/manifests/namespaces.yaml` định nghĩa cả sáu. Pod của ứng dụng vào
`kalapa`; Argo CD đưa chúng vào đó qua
`deploy/argocd/apps/10-services.yaml:52` (`destination.namespace`).

---

## 3. Thực hành

### 3.1 Giải phẫu một Deployment thật

```bash
kubectl -n kalapa get deploy kyc
kubectl -n kalapa describe deploy kyc | head -40
```

Trong output `describe`, tìm dòng `NewReplicaSet`. Đó là RS đang hoạt động.

```bash
kubectl -n kalapa get rs
kubectl -n kalapa get pods --show-labels
```

Để ý: tên ReplicaSet = tên Deployment + mã băm của pod template. Đổi template
→ mã băm đổi → RS mới. Đó là cách Deployment biết "template đã đổi".

### 3.2 Theo dõi một rolling update từng bước

```bash
# Terminal 1
kubectl -n kalapa get pods -l app.kubernetes.io/name=kyc -w

# Terminal 2
kubectl -n kalapa rollout restart deploy/kyc
kubectl -n kalapa rollout status deploy/kyc
```

Ở terminal 1 bạn sẽ thấy đúng trình tự ở mục 1.3: pod mới `Pending` →
`ContainerCreating` → `Running 0/1` → `Running 1/1`, **rồi** pod cũ mới
`Terminating`.

Số `0/1` và `1/1` là số container **đã sẵn sàng** trên tổng số container.
Readiness probe quyết định con số đó (bài 07).

### 3.3 Rollback

```bash
kubectl -n kalapa rollout history deploy/kyc
kubectl -n kalapa rollout undo deploy/kyc
kubectl -n kalapa rollout status deploy/kyc
```

Lưu ý: nếu Argo CD đang chạy với `selfHeal`, nó sẽ hoàn nguyên việc rollback
của bạn trong ~2 phút. Đó là tính năng, không phải lỗi (bài 12).

### 3.4 Chơi với label — bài tập hay nhất của bài này

```bash
# Xem selector mà ReplicaSet dùng
kubectl -n kalapa get rs -l app.kubernetes.io/name=kyc \
  -o jsonpath='{.items[0].spec.selector.matchLabels}' | jq

# Lấy tên một pod
POD=$(kubectl -n kalapa get pod -l app.kubernetes.io/name=kyc -o name | head -1)
echo $POD
```

Giờ **gỡ pod đó khỏi quyền quản lý của ReplicaSet** bằng cách đổi nhãn:

```bash
kubectl -n kalapa label $POD app.kubernetes.io/name=kyc-mo-coi --overwrite
kubectl -n kalapa get pods -l app.kubernetes.io/name=kyc
kubectl -n kalapa get pods -l app.kubernetes.io/name=kyc-mo-coi
```

Chuyện gì xảy ra? ReplicaSet đếm pod khớp selector, thấy thiếu một, **tạo pod
mới**. Còn pod bạn vừa đổi nhãn thì vẫn chạy nhưng giờ **không ai quản** — nó
mồ côi.

Đây là kỹ thuật debug thật: tách một pod đang lỗi ra để mổ xẻ mà không làm
gián đoạn dịch vụ. Dọn dẹp:

```bash
kubectl -n kalapa delete $POD
```

### 3.5 Tự gây ra lỗi selector bất biến

```bash
kubectl -n kalapa patch deploy kyc --type merge \
  -p '{"spec":{"selector":{"matchLabels":{"app.kubernetes.io/name":"kyc","them":"nhan-moi"}}}}'
```

Bạn sẽ nhận: `field is immutable`. Hãy nhớ thông báo này — gặp nó nghĩa là
bạn đã đụng vào selector và cách sửa duy nhất là xoá Deployment rồi tạo lại.

### 3.6 Nhìn một Pod không qua Deployment

```bash
kubectl -n kalapa run tam --image=nginx:alpine --restart=Never
kubectl -n kalapa get pod tam
kubectl -n kalapa delete pod tam
kubectl -n kalapa get pod tam     # -> NotFound, không ai tạo lại
```

So sánh với thí nghiệm ở bài 03 mục 3.3. Đó là khác biệt giữa Pod trần và Pod
thuộc Deployment.

---

## 4. Tự kiểm

- [ ] Pod là gì? Các container trong một Pod chia sẻ những gì?
- [ ] Khi nào thì nên có hơn một container trong một Pod?
- [ ] Deployment → ReplicaSet → Pod: mỗi lớp làm gì?
- [ ] Vì sao ReplicaSet cũ được giữ lại sau khi update xong?
- [ ] `maxSurge: 1` và `maxUnavailable: 0` nghĩa là gì? Phải trả giá gì?
- [ ] Vì sao `maxUnavailable: 1` với `replicas: 1` là một lỗi?
- [ ] ReplicaSet tìm pod của nó bằng cách nào? (không phải bằng danh sách ID)
- [ ] Vì sao `selector` không được chứa nhãn `version`?
- [ ] Nếu bạn đổi nhãn một pod cho khác selector thì chuyện gì xảy ra?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Tạo Pod trần ở production | Chết là xong, không ai bật lại |
| Nhét `version` vào selector | Lần nâng cấp đầu tiên hỏng với `field is immutable` |
| Nghĩ `kubectl delete pod` là xoá được | Deployment tạo lại ngay. Phải scale về 0 hoặc xoá Deployment |
| Tin vào IP của Pod | IP đổi mỗi lần pod được tạo lại. Luôn dùng Service (bài 05) |
| Không đặt `revisionHistoryLimit` | 10 ReplicaSet chết cho mỗi service, nằm mãi trong etcd |
| Tưởng `1/1` nghĩa là app đã sẵn sàng | `1/1` nghĩa là readiness probe đạt. Nếu probe viết sai thì nó chẳng nói lên gì (bài 07) |

---

**Bài tiếp:** [05 — Service, DNS, Ingress](05-service-dns-ingress.md)
