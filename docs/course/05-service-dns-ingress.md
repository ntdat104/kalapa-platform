# 05 — Service, DNS, Ingress

> **Bài này trả lời:** Pod có IP đổi liên tục, vậy chúng gọi nhau kiểu gì? Làm
> sao để người dùng ngoài Internet vào được?
>
> **Cần xong bài:** [04](04-pod-va-deployment.md)

---

## 1. Lý thuyết

### 1.1 Vấn đề

Pod có IP. Nhưng:

- Pod chết và tái tạo → **IP mới**
- Scale lên 3 bản → **3 IP khác nhau**, gọi cái nào?
- Rolling update → trong vài giây có cả IP cũ lẫn IP mới

Bạn không bao giờ được ghi IP của pod vào cấu hình. Bạn cần một **địa chỉ bền
vững** đứng trước một **tập pod thay đổi liên tục**. Đó là Service.

### 1.2 Service

Service làm ba việc:

1. **Một IP ảo cố định** (ClusterIP) không bao giờ đổi suốt đời Service
2. **Một tên DNS** để gọi thay vì nhớ IP
3. **Cân bằng tải** sang các pod đang *sẵn sàng*

```
        Service "kyc"  (ClusterIP 10.96.4.20, không bao giờ đổi)
                │
     ┌──────────┼──────────┐
     ▼          ▼          ▼
  Pod A      Pod B      Pod C      <- IP đổi liên tục, không ai quan tâm
 (Ready)    (Ready)   (NOT Ready)  <- pod C bị loại khỏi vòng xoay
```

Từ **Ready** ở đó rất quan trọng. Service chỉ gửi traffic tới pod đã qua
readiness probe. Đây là mối nối giữa bài này và bài 07.

Service tìm pod của nó bằng **label selector**, giống hệt ReplicaSet:

```yaml
kind: Service
spec:
  selector:
    app.kubernetes.io/name: kyc    # mọi pod có nhãn này
  ports:
    - port: 80          # port của Service
      targetPort: http  # port trên container (có thể gọi theo tên)
```

> **Service hoàn toàn độc lập với Deployment.** Nó không biết Deployment tồn
> tại; nó chỉ chọn pod theo nhãn. Nếu selector của Service viết sai một ký tự
> thì nó không chọn được pod nào, và bạn nhận 503 trong khi `kubectl get pods`
> hiện toàn màu xanh. Đây là lỗi hay gặp số một.

### 1.3 Endpoints — nơi sự thật nằm

Đằng sau mỗi Service là một object **EndpointSlice** chứa danh sách IP thật
của các pod đang Ready. Khi gỡ lỗi, đây là nơi phải nhìn:

```bash
kubectl -n kalapa get endpoints kyc
```

- **Có IP** → Service đã tìm thấy pod
- **Rỗng** → hoặc selector sai, hoặc không pod nào Ready

Phân biệt được hai trường hợp đó tiết kiệm cho bạn rất nhiều giờ.

### 1.4 DNS trong cluster

CoreDNS chạy trong `kube-system` và phân giải tên theo khuôn:

```
<service>.<namespace>.svc.cluster.local
```

Mỗi pod có `/etc/resolv.conf` với `search` path, nên viết ngắn cũng được:

| Viết | Phân giải được từ đâu |
|---|---|
| `kyc` | chỉ từ trong namespace `kalapa` |
| `kyc.kalapa` | **từ mọi namespace** ← nên dùng cái này |
| `kyc.kalapa.svc.cluster.local` | luôn đúng, nhưng dài |

> Repo này dùng dạng hai nhãn `kyc.kalapa`. Dạng một nhãn hoạt động trong lúc
> bạn thử nghiệm ở cùng namespace rồi hỏng khi có thứ gì đó gọi từ namespace
> khác — một kiểu hỏng rất khó chịu vì "nó vẫn chạy mà".

### 1.5 Các loại Service

| Loại | Làm gì | Dùng khi nào |
|---|---|---|
| `ClusterIP` | IP nội bộ, chỉ trong cluster | **Mặc định, và đúng cho hầu hết mọi thứ** |
| `NodePort` | Mở một port (30000–32767) trên *mọi* node | Thử nghiệm nhanh |
| `LoadBalancer` | Nhà cung cấp cloud cấp một LB thật | Production trên cloud |
| `ExternalName` | Bí danh DNS tới tên ngoài | Trỏ tới dịch vụ bên ngoài |

Thêm một loại đặc biệt: **Headless** (`clusterIP: None`) — không IP ảo, DNS
trả về thẳng IP của từng pod. Dùng cho StatefulSet, nơi bạn cần địa chỉ *từng*
instance (bài 09).

### 1.6 Vì sao cần Ingress

`LoadBalancer` giải quyết được "ra ngoài Internet", nhưng mỗi Service một LB
là mỗi Service một địa chỉ IP công cộng và một hoá đơn. Mười service là mười
LB.

**Ingress** là một LB dùng chung, định tuyến theo **hostname** và **đường dẫn**
ở tầng HTTP:

```
                    một địa chỉ IP công cộng
                            │
                   ┌────────▼─────────┐
                   │ Ingress Controller│   (NGINX)
                   └────────┬─────────┘
          ┌─────────────────┼─────────────────┐
          ▼                 ▼                 ▼
  Host: api.kalapa.local  grafana.kalapa.local  argocd.kalapa.local
  Path: /api              /                     /
          │                 │                   │
     Service gateway   Service grafana    Service argocd-server
```

Hai thứ tên gần giống nhau nhưng khác hẳn:

- **Ingress** — object YAML mô tả luật định tuyến. Tự nó không làm gì cả.
- **Ingress Controller** — chương trình thật (NGINX, Traefik…) đọc các object
  Ingress và cấu hình chính nó theo đó.

> Không cài controller mà vẫn tạo Ingress thì object được chấp nhận và **không
> có gì xảy ra**. Không có lỗi nào. Đây là bẫy kinh điển của người mới.

`ingressClassName` là thứ nói "Ingress này do controller nào xử lý" — cần khi
cluster có nhiều controller.

---

## 2. Trong repo này nằm đâu

### Service

`deploy/charts/go-service/templates/service.yaml`

```yaml
spec:
  type: {{ .Values.service.type }}      # ClusterIP
  ports:
    - name: http
      port: {{ .Values.service.port }}   # 80
      targetPort: http                   # tên port trên container
    - name: admin
      port: {{ .Values.ports.admin }}    # 9090
      targetPort: admin
  selector:
    {{- include "go-service.selectorLabels" . | nindent 4 }}
```

Hai chi tiết đáng chú ý:

**`targetPort: http` dùng *tên*, không dùng số.** Tên đó được khai báo ở
`deployment.yaml` trong phần `ports:` của container. Nhờ vậy đổi số port chỉ
cần sửa một chỗ.

**Service dùng chung `selectorLabels` với Deployment** — cùng một hàm template.
Đó là cách chart đảm bảo hai bên không bao giờ lệch nhau.

### DNS được dùng ở đâu

`deploy/charts/kalapa-config/values.yaml`, trong khối `config:`:

```yaml
  database:
    url: postgres://kalapa-db-rw.data:5432/kalapa?sslmode=disable
  kafka:
    brokers:
      - kalapa-kafka-bootstrap.kafka:9092
  services:
    kyc: http://kyc.kalapa
    scoring: http://scoring.kalapa
```

Mọi địa chỉ đều là `<service>.<namespace>`. Không có IP nào bị ghi cứng.

`kalapa-db-rw` đặc biệt đáng chú ý — nó là Service do CloudNativePG tạo, luôn
trỏ tới primary hiện tại. Khi failover, operator chuyển hướng Service và ứng
dụng **không cần biết gì** (bài 09).

### Ingress

`deploy/charts/go-service/templates/ingress.yaml` — toàn bộ file bọc trong
`{{- if .Values.ingress.enabled -}}`, nên mặc định không sinh ra gì.

Chỉ gateway bật nó. `deploy/charts/gateway/values.yaml`:

```yaml
  ingress:
    enabled: true
    className: nginx
    host: api.kalapa.local
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "30"
      nginx.ingress.kubernetes.io/proxy-body-size: "2m"
    paths:
      - path: /api
        pathType: Prefix
```

`kyc` và `scoring` để `enabled: false`. **Chúng chỉ với tới được qua gateway.**
Cho mỗi service một Ingress riêng là cách một API nội bộ lọt ra Internet.

Và trong template, dòng này đáng đọc kỹ:

```yaml
              service:
                name: {{ $fullName }}
                port:
                  number: {{ $svcPort }}    # LUÔN là port http
```

Trỏ Ingress vào port admin sẽ công khai `/metrics` và các endpoint health ra
Internet — tức là công khai cấu trúc hệ thống của bạn.

### Ingress controller đến từ đâu

`scripts/00-start-cluster.sh`:

```bash
for addon in ingress metrics-server storage-provisioner default-storageclass; do
  minikube addons enable "$addon" -p "$CLUSTER_NAME" >/dev/null 2>&1 && ok "$addon"
done
```

Repo dùng addon của minikube thay vì tự cài chart, vì nó đơn giản hơn và tiết
kiệm RAM.

---

## 3. Thực hành

### 3.1 Giải phẫu một Service

```bash
kubectl -n kalapa get svc
kubectl -n kalapa describe svc kyc
```

Trong output, so sánh hai dòng: `Selector:` và `Endpoints:`. Nếu `Endpoints`
rỗng thì có vấn đề.

```bash
# Sự thật nằm ở đây
kubectl -n kalapa get endpoints kyc
kubectl -n kalapa get endpointslices -l kubernetes.io/service-name=kyc -o yaml | yq '.items[0].endpoints'
```

### 3.2 Chứng minh DNS hoạt động

```bash
kubectl -n kalapa run dns-thu --rm -it --image=busybox:1.36 --restart=Never -- sh
```

Trong shell đó:

```sh
cat /etc/resolv.conf          # xem search path
nslookup kyc                  # dạng ngắn — chạy được vì cùng namespace
nslookup kyc.kalapa           # dạng hai nhãn
nslookup kalapa-db-rw.data    # service ở namespace khác
nslookup kyc.observability    # -> KHÔNG tìm thấy, vì namespace sai
wget -qO- http://kyc.kalapa/kyc/applications
exit
```

Bài tập quan trọng ở đây là lệnh `nslookup kyc.observability` thất bại. Hãy
hiểu rõ vì sao.

### 3.3 Tự tay phá Service — bài thực hành giá trị nhất

```bash
# Làm hỏng selector
kubectl -n kalapa patch svc kyc --type merge \
  -p '{"spec":{"selector":{"app.kubernetes.io/name":"sai-ten"}}}'

kubectl -n kalapa get endpoints kyc        # -> <none>
kubectl -n kalapa get pods                 # -> vẫn 1/1 Running, toàn màu xanh!
```

Đây chính là kiểu hỏng khiến người ta mất hàng giờ: **pod hoàn toàn khoẻ mạnh,
nhưng không ai tới được chúng.** Thử gọi:

```bash
kubectl -n kalapa run thu --rm -it --image=busybox:1.36 --restart=Never -- \
  wget -qO- --timeout=5 http://kyc.kalapa/kyc/applications
# -> timeout hoặc connection refused
```

Sửa lại:

```bash
kubectl -n kalapa patch svc kyc --type merge \
  -p '{"spec":{"selector":{"app.kubernetes.io/name":"kyc","app.kubernetes.io/instance":"kyc"}}}'
kubectl -n kalapa get endpoints kyc        # -> IP đã quay lại
```

> **Rút ra:** khi gặp 502/503, bước đầu tiên luôn là `kubectl get endpoints`.
> Rỗng nghĩa là vấn đề ở selector hoặc ở readiness, không phải ở ứng dụng.

### 3.4 Ingress

```bash
kubectl -n kalapa get ingress
kubectl -n kalapa describe ingress gateway
```

Để ý `Address:` — đó là IP của node. Rồi:

```bash
make hosts      # cần sudo, trỏ *.kalapa.local về IP đó
curl -s http://api.kalapa.local/api/version
```

Nếu `make hosts` không chạy được, dùng cách thủ công:

```bash
kubectl -n ingress-nginx port-forward svc/ingress-nginx-controller 18080:80 &
curl -s -H 'Host: api.kalapa.local' http://localhost:18080/api/version
kill %1
```

Header `Host` là thứ Ingress dùng để chọn luật — đó là lý do cách thủ công này
hoạt động.

### 3.5 Chứng minh service nội bộ không ra được Internet

```bash
# gateway có Ingress
kubectl -n kalapa get ingress gateway
# kyc thì không
kubectl -n kalapa get ingress kyc        # -> NotFound

# Nhưng từ bên trong cluster thì gọi được
kubectl -n kalapa run thu --rm -it --image=busybox:1.36 --restart=Never -- \
  wget -qO- http://kyc.kalapa/kyc/applications
```

Đây là mô hình đúng: một cửa vào duy nhất, mọi thứ khác chỉ nội bộ.

### 3.6 Nhìn vào cấu hình NGINX được sinh ra

```bash
kubectl -n ingress-nginx exec deploy/ingress-nginx-controller -- \
  cat /etc/nginx/nginx.conf | grep -A12 "api.kalapa.local" | head -20
```

Bạn đang nhìn thấy object Ingress đã được dịch thành cấu hình NGINX thật. Đây
là lúc "Ingress Controller đọc Ingress và tự cấu hình" trở nên cụ thể.

---

## 4. Tự kiểm

- [ ] Vì sao không được ghi IP của Pod vào cấu hình?
- [ ] Service làm ba việc gì?
- [ ] Service tìm pod bằng cách nào?
- [ ] `Endpoints` rỗng nghĩa là gì? Hai nguyên nhân có thể?
- [ ] `kyc` và `kyc.kalapa` khác nhau ở đâu? Nên dùng cái nào, vì sao?
- [ ] Bốn loại Service, mỗi loại dùng khi nào?
- [ ] Ingress khác Ingress Controller thế nào? Thiếu controller thì sao?
- [ ] Vì sao Ingress phải trỏ vào port http chứ không phải port admin?
- [ ] Trong repo này, vì sao chỉ gateway có Ingress?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Selector của Service viết sai | Endpoints rỗng, 503, nhưng pod vẫn xanh. Luôn kiểm tra endpoints trước |
| Dùng tên service dạng một nhãn | Chạy trong cùng namespace, hỏng khi gọi từ namespace khác |
| Tạo Ingress mà chưa cài controller | Object được chấp nhận, không gì xảy ra, không báo lỗi |
| Mỗi service một Ingress | API nội bộ lọt ra Internet |
| Trỏ Ingress vào port admin | Công khai `/metrics` và cấu trúc hệ thống |
| Dùng `NodePort` ở production | Port ngẫu nhiên, không TLS, không định tuyến theo host |
| Quên `pathType` | Mặc định khác nhau tuỳ controller, dẫn tới định tuyến bất ngờ |

---

**Bài tiếp:** [06 — ConfigMap và Secret](06-configmap-va-secret.md)
