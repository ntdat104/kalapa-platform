# 03 — Vì sao cần Kubernetes

> **Bài này trả lời:** Đã có Docker rồi thì còn thiếu gì? Khai báo khác mệnh
> lệnh ở đâu? Cluster gồm những bộ phận nào?
>
> **Cần xong bài:** [02](02-container-va-docker.md)
>
> **Cuối bài này bạn sẽ dựng cluster đầu tiên.**

---

## 1. Lý thuyết

### 1.1 Docker giải quyết đóng gói, không giải quyết vận hành

Bạn đã đóng gói được app thành image. Giờ chạy nó ở production. Những câu hỏi
Docker không trả lời:

| Câu hỏi | Nếu chỉ có Docker |
|---|---|
| Container chết lúc 3 giờ sáng thì ai bật lại? | Bạn, nếu bạn tỉnh |
| Cần 3 bản sao để chịu tải thì đặt ở đâu? | Bạn tự chọn máy, tự nhớ |
| Một máy chủ hỏng thì sao? | Mọi thứ trên đó chết, bạn dựng tay lại |
| Deploy phiên bản mới mà không mất request? | Tự viết script, tự xử lý từng bước |
| Service A gọi service B thế nào khi IP của B đổi liên tục? | Tự dựng service registry |
| Tăng từ 3 lên 10 bản sao? | Tự tìm máy còn chỗ |
| Mật khẩu DB đưa vào container kiểu gì mà không commit vào git? | Tự nghĩ cách |

Mọi câu đều **giải được**. Vấn đề là mỗi công ty tự giải một kiểu, và lời giải
nào cũng hoá thành một đống shell script không ai dám sửa.

**Kubernetes là lời giải chung cho toàn bộ danh sách đó.**

### 1.2 Khác biệt cốt lõi: khai báo thay vì mệnh lệnh

Đây là ý tưởng quan trọng nhất trong cả bộ giáo trình. Nếu chỉ nhớ một điều từ
bài này, hãy nhớ điều này.

**Mệnh lệnh (imperative)** — bạn ra lệnh từng bước:

```bash
docker run -d --name kyc-1 kalapa-kyc:v1
docker run -d --name kyc-2 kalapa-kyc:v1
# kyc-2 chết lúc 3h sáng... và nó nằm chết tới sáng
```

**Khai báo (declarative)** — bạn mô tả kết quả mong muốn:

```yaml
kind: Deployment
spec:
  replicas: 2
  template:
    spec:
      containers:
        - image: kalapa-kyc:v1
```

Bạn không nói "hãy chạy". Bạn nói **"tôi muốn luôn luôn có 2 bản"**. Rồi một
**controller** chạy vòng lặp vô tận:

```
mãi mãi:
    mong muốn = đọc từ API ("2 bản")
    thực tế   = đếm pod đang chạy ("1 bản")
    nếu khác:  hành động để kéo thực tế về gần mong muốn
```

Vòng lặp này gọi là **reconciliation loop** (vòng lặp điều hoà). Nó là trái
tim của Kubernetes, và về sau bạn sẽ gặp lại chính nó ở Argo CD (bài 12) và ở
Operator (bài 09).

Hệ quả thực tế: pod chết lúc 3 giờ sáng thì **9 giây sau có pod mới**, không
ai phải tỉnh dậy.

### 1.3 Cluster gồm những gì

```
┌─────────────────────── CONTROL PLANE (bộ não) ───────────────────────┐
│                                                                       │
│  kube-apiserver     Cửa duy nhất. MỌI thứ đi qua đây. kubectl nói     │
│                     chuyện với nó, controller cũng vậy.                │
│                                                                       │
│  etcd               Cơ sở dữ liệu key-value. Nơi lưu trạng thái        │
│                     mong muốn. Mất etcd = mất cluster.                 │
│                                                                       │
│  kube-scheduler     Quyết định pod mới chạy trên node nào.             │
│                                                                       │
│  controller-manager Chứa các vòng lặp điều hoà có sẵn                  │
│                     (Deployment, ReplicaSet, Node, …).                 │
└───────────────────────────────────────────────────────────────────────┘
                                   │
          ┌────────────────────────┼────────────────────────┐
          ▼                        ▼                        ▼
┌──── NODE 1 ─────┐      ┌──── NODE 2 ─────┐      ┌──── NODE 3 ─────┐
│ kubelet         │      │ kubelet         │      │ kubelet         │
│   nhận lệnh từ  │      │                 │      │                 │
│   apiserver,    │      │                 │      │                 │
│   chạy container│      │                 │      │                 │
│                 │      │                 │      │                 │
│ kube-proxy      │      │ kube-proxy      │      │ kube-proxy      │
│   định tuyến    │      │                 │      │                 │
│                 │      │                 │      │                 │
│ container runtime      │                 │      │                 │
│   (containerd)  │      │                 │      │                 │
│                 │      │                 │      │                 │
│  [pod] [pod]    │      │  [pod] [pod]    │      │  [pod]          │
└─────────────────┘      └─────────────────┘      └─────────────────┘
```

Trên minikube, cả control plane lẫn node nằm chung **một** container Docker.
Trên cluster thật, control plane thường có 3 bản để chịu lỗi.

Điều quan trọng cần nắm: **không thành phần nào nói chuyện trực tiếp với nhau.**
Tất cả đọc/ghi qua apiserver. Kiến trúc đó khiến việc thêm một controller mới
(chính là Operator ở bài 09) chẳng cần sửa gì sẵn có.

### 1.4 Vòng đời một lệnh `kubectl apply`

```
1. kubectl đọc file YAML, gửi HTTP POST tới kube-apiserver
2. apiserver xác thực bạn là ai (authentication)
3. apiserver kiểm tra bạn có quyền không (authorization / RBAC)
4. admission controller soi và có thể từ chối hoặc sửa object
   (đây là chỗ Pod Security Admission chặn pod chạy root — bài 14)
5. apiserver ghi object vào etcd.  <-- ĐẾN ĐÂY lệnh kubectl trả về OK
6. Deployment controller thấy object mới -> tạo ReplicaSet
7. ReplicaSet controller thấy -> tạo Pod (chưa gán node)
8. scheduler thấy pod chưa có node -> chọn node, ghi lại vào etcd
9. kubelet trên node đó thấy -> gọi containerd kéo image và chạy
10. kubelet báo trạng thái ngược về apiserver
```

Hiểu rõ bước 5 rất quan trọng: **`kubectl apply` trả về "created" không có
nghĩa là app đã chạy.** Nó chỉ có nghĩa là mong muốn đã được ghi nhận. Từ bước
6 đến 10 là bất đồng bộ và có thể thất bại — image kéo không được, node hết
RAM, probe không đạt. Đó là lý do bạn luôn phải `kubectl rollout status` hoặc
nhìn `kubectl get pods`.

### 1.5 Object và API

Mọi thứ trong Kubernetes là một **object**, và mọi object có cùng bộ khung:

```yaml
apiVersion: apps/v1        # phiên bản API nào định nghĩa loại này
kind: Deployment           # loại object
metadata:
  name: kyc                # tên, duy nhất trong namespace
  namespace: kalapa        # nhóm logic
  labels: {...}            # nhãn để truy vấn và để selector bám vào
  annotations: {...}       # dữ liệu phụ, không dùng để truy vấn
spec:                      # TRẠNG THÁI MONG MUỐN — bạn viết phần này
  # ...
status:                    # TRẠNG THÁI THỰC TẾ — hệ thống ghi, bạn chỉ đọc
  # ...
```

Cặp `spec` / `status` chính là mô hình khai báo hiện ra thành hình. Bạn viết
`spec`, controller cố làm cho `status` khớp với nó. Khi gỡ lỗi, **`status` là
nơi chứa câu trả lời** — bài 09 có một ví dụ mà thông báo lỗi thật chỉ nằm
trong `status`, không ở đâu khác.

---

## 2. Trong repo này nằm đâu

### Script dựng cluster

`scripts/00-start-cluster.sh` — đọc cả file, nó ngắn và mọi dòng đều có chú
thích lý do.

```bash
minikube start \
  -p "$CLUSTER_NAME" \
  --driver=docker \
  --cpus="$CPUS" \
  --memory="$MEMORY" \
  --kubernetes-version="$K8S_VERSION"
```

Và hai addon được bật:

```bash
for addon in ingress metrics-server storage-provisioner default-storageclass; do
  minikube addons enable "$addon" -p "$CLUSTER_NAME" >/dev/null 2>&1 && ok "$addon"
done
```

| Addon | Vì sao cần |
|---|---|
| `ingress` | Controller cho mọi host `*.kalapa.local` (bài 05) |
| `metrics-server` | `kubectl top`, và HPA. Thiếu nó HPA hiện `<unknown>` mãi mãi (bài 07) |
| `storage-provisioner` | Tự cấp volume cho Postgres và Kafka (bài 09, 10) |

### Namespace — nhóm logic đầu tiên bạn gặp

`deploy/manifests/namespaces.yaml:20`

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kalapa
  labels:
    pod-security.kubernetes.io/enforce: restricted
```

Namespace chia cluster thành các vùng. Trong repo này có sáu vùng, mỗi vùng
một tầng — xem bảng ở [docs/00-architecture.md](../00-architecture.md#namespace).

---

## 3. Thực hành — dựng cluster đầu tiên

### 3.1 Dựng

```bash
cd ~/Documents/resource/kalapa-platform
MEMORY=8g make cluster
```

Mất khoảng 3 phút. Trong lúc chờ, đọc lại sơ đồ control plane ở mục 1.3.

### 3.2 Nhìn vào bộ não

```bash
# Node (ở đây chỉ có một)
kubectl get nodes -o wide

# Chính control plane cũng chạy bằng pod
kubectl -n kube-system get pods
```

Bạn sẽ thấy `etcd-kalapa`, `kube-apiserver-kalapa`, `kube-scheduler-kalapa`,
`kube-controller-manager-kalapa`. Đây là lúc sơ đồ ở mục 1.3 trở thành thứ sờ
được.

### 3.3 Tự tay chứng kiến vòng lặp điều hoà

Đây là bài thực hành quan trọng nhất của bài này. Làm thật.

```bash
# Tạo một Deployment đơn giản
kubectl create deployment nginx-thu --image=nginx:alpine --replicas=3
kubectl get pods -l app=nginx-thu

# Terminal 2: theo dõi liên tục
kubectl get pods -l app=nginx-thu -w
```

Giờ ở terminal 1, giết một pod:

```bash
kubectl delete pod -l app=nginx-thu --field-selector=status.phase=Running \
  --wait=false | head -1
```

Nhìn terminal 2. Pod bị xoá chuyển `Terminating`, và **gần như lập tức một pod
mới xuất hiện**. Không ai bảo nó làm thế. ReplicaSet controller chỉ đơn giản
thấy "mong muốn 3, thực tế 2" và hành động.

Thử mạnh tay hơn — xoá sạch:

```bash
kubectl delete pod -l app=nginx-thu --all
kubectl get pods -l app=nginx-thu
```

Ba pod mới mọc lên ngay. **Bạn không xoá được pod của một Deployment.** Muốn
xoá thật thì phải đổi mong muốn:

```bash
kubectl delete deployment nginx-thu
```

### 3.4 Nhìn spec và status

```bash
kubectl create deployment nginx-thu --image=nginx:alpine --replicas=2
kubectl get deployment nginx-thu -o yaml | yq '.spec.replicas'   # mong muốn
kubectl get deployment nginx-thu -o yaml | yq '.status'          # thực tế
kubectl delete deployment nginx-thu
```

### 3.5 Xem apiserver nhận gì

```bash
# -v=8 in ra toàn bộ HTTP request kubectl gửi đi
kubectl get pods -n kube-system -v=8 2>&1 | grep -E "^I.*(GET|Request Headers|Response Status)" | head
```

Bạn đang nhìn thấy kubectl chỉ là một HTTP client. Không có phép màu nào.

---

## 4. Tự kiểm

- [ ] Kể năm việc Kubernetes làm mà Docker không làm.
- [ ] Khai báo khác mệnh lệnh ở đâu? Cho ví dụ cụ thể.
- [ ] Vòng lặp điều hoà làm gì? Viết nó ra bằng ba dòng giả mã.
- [ ] Bốn thành phần của control plane, mỗi cái làm gì?
- [ ] kubelet chạy ở đâu và nhiệm vụ là gì?
- [ ] `kubectl apply` trả về OK nghĩa là app đã chạy chưa? Vì sao?
- [ ] `spec` và `status` khác nhau thế nào? Ai ghi cái nào?
- [ ] Vì sao không xoá được pod thuộc một Deployment?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| "`kubectl apply` xong là deploy xong" | Mới chỉ ghi được mong muốn. Luôn kiểm tra `rollout status`. |
| Dùng `kubectl run`/`create` ở production | Mệnh lệnh = không tái lập được. Mọi thứ phải là YAML trong git (bài 12). |
| Sửa trực tiếp bằng `kubectl edit` | Lần apply sau sẽ ghi đè. Với Argo CD thì bị hoàn nguyên sau 2 phút. |
| Nghĩ pod là thứ bền vững | Pod là thứ dùng rồi vứt. Thiết kế phải chịu được việc pod biến mất bất cứ lúc nào. |
| Tin vào `kubectl get pods` một lần | Trạng thái thay đổi liên tục. Dùng `-w` để theo dõi. |

---

**Bài tiếp:** [04 — Pod và Deployment](04-pod-va-deployment.md)
