# 02 — Kubernetes chuyên sâu

Những phần của Kubernetes mà repo này động tới, sắp theo thứ tự chúng hay cắn
bạn. Mỗi mục đều nêu file cài đặt nó, và phần lớn kết thúc bằng một câu lệnh
để chạy trên cluster đang sống.

---

## 1. Probe: ba câu hỏi, ba câu trả lời

Kubernetes hỏi ba câu khác nhau. Trả lời cả ba bằng cùng một endpoint là
nguyên nhân phổ biến nhất của những sự cố tự gây ra.

| Probe | Câu hỏi | Hành động khi hỏng | Tuyệt đối không được phụ thuộc |
|---|---|---|---|
| `startupProbe` | "khởi động xong chưa?" | chờ tiếp, rồi restart | thứ gì chậm mà nó không kiểm soát |
| `livenessProbe` | "process có bị treo cứng không?" | **restart container** | bất kỳ phụ thuộc bên ngoài nào |
| `readinessProbe` | "nhận traffic được chưa?" | **gỡ khỏi Service endpoints** | không gì cả — chỗ của phụ thuộc là đây |

Cài đặt: `internal/platform/httpx/server.go`, cấu hình trong
`deploy/charts/go-service/values.yaml` ở mục `probes:`.

### Vì sao liveness không được kiểm tra database

```go
// httpx/server.go
mux.HandleFunc("GET /healthz/live", func(w http.ResponseWriter, r *http.Request) {
    // Cố ý không phụ thuộc gì.
    if s.live.Load() { ... }
})
```

Giả sử liveness có ping Postgres. Postgres chập chờn 30 giây. Mọi replica của
mọi service cùng lúc trượt liveness và bị restart. Tất cả quay lại ở trạng
thái nguội, cùng lúc kết nối lại, và cơn bão kết nối đó giữ cho database tiếp
tục chết. Một cú chập 30 giây đã biến thành sự cố 10 phút, mà nguyên nhân hoàn
toàn là cái health check.

Readiness kiểm tra Postgres thì lại đúng: những pod đó rời khỏi load balancer,
ngừng nhận traffic, và quay lại khi database hồi. Không restart, không khởi
động nguội, không bão.

### Vì sao cần `startupProbe`

Không có nó, bạn cần một probe vừa chịu được lần khởi động đầu 90 giây *vừa*
bắt được treo nhanh. Hai yêu cầu đó mâu thuẫn. `startupProbe` tách chúng ra:
nó được ngân sách rộng rãi (`failureThreshold: 45` × `periodSeconds: 2` = 90
giây), và liveness chưa bắt đầu cho tới khi nó đạt — rồi mới chạy chặt
(3 × 10 giây = 30 giây) cho cả phần đời còn lại của pod.

```bash
kubectl -n kalapa get pod -l app.kubernetes.io/name=kyc \
  -o jsonpath='{.items[0].spec.containers[0].startupProbe}' | jq
```

---

## 2. Resources và QoS

```yaml
resources:
  requests: { cpu: 50m, memory: 48Mi }   # scheduler giữ chỗ chừng này
  limits:   { cpu: 300m, memory: 96Mi }  # trần cgroup
```

Hai sự thật không đối xứng:

- **Vượt limit bộ nhớ = OOMKill.** Lập tức, không cảnh báo, exit code 137.
- **Vượt limit CPU = bị throttle.** Process bị làm chậm, không bao giờ bị giết.
  Nó trông giống latency, không giống lỗi.

### Các lớp QoS

| Lớp | Điều kiện | Thứ tự bị evict |
|---|---|---|
| `Guaranteed` | requests == limits, cho mọi tài nguyên và mọi container | cuối cùng |
| `Burstable` | có requests, nhỏ hơn limits | ở giữa |
| `BestEffort` | không khai báo gì | **đầu tiên** |

YAS để `resources: {}`, nghĩa là mọi service rơi vào `BestEffort` — bị evict
đầu tiên khi node thiếu tài nguyên, và vô hình với HPA (utilisation là phần
trăm *của request*, mà không có request nào).

```bash
kubectl -n kalapa get pods -o custom-columns=\
NAME:.metadata.name,QOS:.status.qosClass
```

### Phần riêng của Go

Một process Go không đọc cgroup. Để mặc, nó thấy *10 CPU và 16 GB của node* và
tự cấu hình theo đó, trong khi bị nhốt trong hộp 300 m / 96 Mi.

```yaml
# deploy/charts/go-service/templates/deployment.yaml
- name: GOMAXPROCS
  valueFrom:
    resourceFieldRef: { resource: limits.cpu, divisor: "1" }
- name: GOMEMLIMIT
  value: "72MiB"
```

Không có dòng `GOMAXPROCS`, Go khởi 10 OS thread tranh nhau 0,3 core — thrash
chuyển ngữ cảnh, và latency trông y hệt một vấn đề mạng.

`GOMEMLIMIT` là giới hạn *mềm*: khi heap tiến gần nó thì GC chạy mạnh hơn thay
vì để kernel OOM-kill pod. Nó được đặt thấp hơn `limits.memory` khoảng 25% vì
stack của goroutine và cấu trúc của runtime nằm ngoài heap, nên cũng nằm ngoài
tầm kiểm soát của GOMEMLIMIT.

```bash
kubectl -n kalapa port-forward deploy/kyc 9090:9090 &
curl -s localhost:9090/metrics | grep -E 'go_memstats_heap_inuse|go_goroutines'
```

---

## 3. Tắt mềm: năm thứ phải khớp nhau

Đây là chỗ phân chia giữa "máy tôi chạy được" và "deploy nào cũng rớt vài
request". Trình tự, và chỗ cài đặt từng mảnh:

```
  t=0   kubelet quyết định chấm dứt pod
        │
        ├─► endpoints controller bắt đầu gỡ pod khỏi Service
        │   (bất đồng bộ — kube-proxy và ingress-nginx biết tin muộn hơn
        │    vài chục đến vài trăm mili-giây)
        │
        └─► preStop hook chạy             [chart: lifecycle.preStop.sleep = 5s]
            │   container VẪN ĐANG PHỤC VỤ ở đây — đó chính là mục đích
            │
  t=5   SIGTERM được gửi                  [Go: signal.NotifyContext]
        │
        ├─► readiness chuyển sang false    [httpx: s.ready.Store(false)]
        ├─► srv.Shutdown() ngừng nhận mới, xả nốt request đang chạy [ngân sách 20s]
        └─► trace exporter flush           [main: shutdownTracing]
        │
  t=25  process thoát sạch sẽ
  t=45  kubelet sẽ SIGKILL                 [chart: terminationGracePeriodSeconds]
```

Phép tính phải đúng:

```
preStopSleepSeconds (5) + shutdownTimeout (20) < terminationGracePeriodSeconds (45)
```

Sai là kubelet SIGKILL giữa chừng, cắt ngang các request đang chạy.

**Vì sao cần khoảng dừng preStop.** Việc gỡ endpoint và việc gửi SIGTERM diễn
ra *song song*, không nối tiếp. Không có khoảng dừng, process ngừng nhận kết
nối trong khi Ingress vẫn đang định tuyến tới nó — đó đúng là mấy con 502 mà
người ta thấy ở mọi lần rolling update và thường đổ cho load balancer.

**Vì sao dùng `sleep:` chứ không `exec: ["sh","-c","sleep 5"]`.** Image là
distroless: không có shell. Dạng exec sẽ thất bại âm thầm và bạn ngồi debug
mấy con 502 ma. Lifecycle action `sleep` gốc (GA từ v1.33) không cần shell.

```bash
# Xem tận mắt. Terminal 1:
kubectl -n kalapa rollout restart deploy/gateway
# Terminal 2, dội vào endpoint — phải không có lỗi nào:
./scripts/load.sh 60 10
```

---

## 4. Rolling update

```yaml
strategy:
  rollingUpdate: { maxUnavailable: 0, maxSurge: 1 }
```

`maxUnavailable: 0` nghĩa là: tạo pod mới, chờ nó đạt readiness, *rồi* mới gỡ
một pod cũ. Năng lực phục vụ không bao giờ tụt dưới số replica đã khai báo.
Cái giá là phải có chỗ trống cho thêm một pod trong lúc roll.

Lỗi hay gặp là `maxUnavailable: 1` với `replicas: 1` — pod duy nhất bị gỡ
trước khi bản thay thế sẵn sàng, tạo ra một cửa sổ mất dịch vụ chắc chắn ở mọi
lần deploy.

```bash
kubectl -n kalapa rollout history deploy/kyc
kubectl -n kalapa rollout undo deploy/kyc --to-revision=1
```

`revisionHistoryLimit: 3` giới hạn bạn `undo` lùi được bao xa. Giá trị mặc
định 10 để lại mười ReplicaSet chết cho mỗi service trong etcd.

---

## 5. HPA

```yaml
autoscaling:
  enabled: true
  targetCPUUtilizationPercentage: 70
```

Ba điều không hiển nhiên:

**Utilisation là phần trăm của request, không phải của limit.** Với
`requests.cpu: 50m` và mục tiêu 70%, HPA scale ra ở mức 35 m CPU thật — tức
12% của limit 300 m. Đặt request quá thấp làm HPA nhạy quá mức.

**Không được đặt `replicas` khi HPA đang bật.** Chart bỏ hẳn trường đó trong
trường hợp này:

```yaml
{{- if not .Values.autoscaling.enabled }}
replicas: {{ .Values.replicaCount }}
{{- end }}
```

Đặt cả hai thì mỗi lần đồng bộ Helm sẽ kéo số replica về `replicaCount`, xoá
quyết định của HPA — và dưới `selfHeal` của Argo CD thì chuyện đó xảy ra mỗi
hai phút.

**Ổn định bất đối xứng.** Scale lên nhanh (30 giây) vì cơn tải đang gây đau
rồi; scale xuống chậm (300 giây) vì thu hẹp ngay sau một đợt tăng chỉ tái tạo
đợt tăng đó trên số pod còn lại.

**Nó cần metrics-server.** Không có thì HPA hiển thị `<unknown>/70%` mãi mãi.

```bash
kubectl -n kalapa get hpa -w
kubectl top pods -n kalapa
```

---

## 6. PodDisruptionBudget

PDB chỉ ràng buộc gián đoạn **tự nguyện**: drain node, cluster-autoscaler thu
hẹp, `kubectl drain`. Nó không làm gì với kernel panic, OOM kill, hay node mất
điện. Nó không thay thế cho việc có nhiều hơn một replica.

Cái bẫy, và cũng là lý do nó bị tắt mặc định ở đây:

```yaml
podDisruptionBudget:
  enabled: false     # với replicaCount: 1, minAvailable: 1 làm drain treo vĩnh viễn
```

`minAvailable: 1` với một replica nghĩa là evict pod duy nhất sẽ vi phạm ngân
sách, nên lệnh evict bị từ chối — vĩnh viễn. Trên lab một node thì
`kubectl drain` sẽ không bao giờ trả về.

```bash
# Bật trong values.yaml cùng với replicaCount: 2, rồi:
kubectl -n kalapa get pdb
kubectl drain kalapa --ignore-daemonsets --delete-emptydir-data --dry-run=server
```

---

## 7. NetworkPolicy

**Quy tắc giải thích mọi sự bối rối:** một pod là *mặc định cho phép* cho tới
khi có một NetworkPolicy chọn nó. Ngay khoảnh khắc có một policy chọn, pod đó
trở thành *mặc định từ chối* cho các hướng (`Ingress`/`Egress`) mà policy liệt
kê. Nghĩa là policy đầu tiên bạn viết sẽ khoá pod lại, và sau đó bạn phải mở
lại mọi luồng nó cần — bắt đầu bằng DNS.

```yaml
# deploy/charts/go-service/templates/networkpolicy.yaml
egress:
  - to:
      - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: kube-system } }
        podSelector: { matchLabels: { k8s-app: kube-dns } }
    ports: [{ port: 53, protocol: UDP }, { port: 53, protocol: TCP }]
```

Quên khối đó là mọi lời gọi ra ngoài đều chết ở bước phân giải tên, trong khi
log thì đổ lỗi cho service đích.

**CNI mặc định của minikube không thực thi NetworkPolicy.** Các object được
chấp nhận rồi bị bỏ qua. Một policy bạn chưa kiểm chứng là một policy bạn
không có:

```bash
# Chứng minh nó KHÔNG được thực thi trên CNI mặc định:
kubectl -n kalapa run probe --rm -it --image=busybox --restart=Never -- \
  wget -qO- --timeout=3 http://kyc/kyc/applications    # thành công -> không thực thi

# Để nó có thật, dựng lại cluster với Calico:
minikube delete -p kalapa && CNI=calico make cluster
```

Cũng để ý `ingress-nginx` trong danh sách cho phép: controller chạy ở namespace
riêng của nó, nên thiếu luật đó thì Ingress không với tới gateway được nữa và
bạn nhận 503 từ NGINX trong khi pod phía sau vẫn khoẻ.

---

## 8. Pod Security Admission

PSA thay thế PodSecurityPolicy. Nó được thực thi theo label của namespace, với
ba profile:

| Profile | Cho phép |
|---|---|
| `privileged` | mọi thứ |
| `baseline` | chặn những thứ hiển nhiên: host namespace, container privileged, hostPath |
| `restricted` | thêm: bắt buộc non-root, không leo thang quyền, bỏ hết capability, phải có seccomp profile |

```yaml
# deploy/manifests/namespaces.yaml
metadata:
  name: kalapa
  labels:
    pod-security.kubernetes.io/enforce: restricted
```

securityContext của chart `go-service` được viết để thoả `restricted`:

```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532                 # phải khớp dòng USER trong build/Dockerfile
  seccompProfile: { type: RuntimeDefault }
securityContext:
  allowPrivilegeEscalation: false
  capabilities: { drop: ["ALL"] }
  readOnlyRootFilesystem: true
```

Ba trong số này đáng hiểu chứ không nên chỉ chép:

- **`runAsUser: 65532`** phải khớp dòng `USER` trong Dockerfile. Nếu lệch nhau,
  container không đọc được chính file nhị phân của nó và crash-loop với lỗi
  `permission denied`.
- **`readOnlyRootFilesystem: true`** đòi phải có chỗ ghi được cho file tạm,
  nên chart mount một `emptyDir` vào `/tmp` khi bật nó.
- **`capabilities: drop: ["ALL"]`** — một binary Go tĩnh không cần capability
  nào, kể cả bộ mặc định. Thứ gì hỏng sau dòng này là thứ cần thêm lại đúng
  một capability cụ thể, và bạn nên muốn biết đó là cái nào.

`observability` bị gắn nhãn `privileged` vì node-exporter mount filesystem của
host và dùng chung network của host. Việc bị buộc phải ghi ngoại lệ đó ra một
file mới là phần hữu ích.

```bash
# Chứng minh nó được thực thi: lệnh này phải bị từ chối ngay ở admission.
kubectl -n kalapa run bad --image=nginx --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"bad","image":"nginx","securityContext":{"privileged":true}}]}}'
```

---

## 9. Service discovery và DNS

```
<service>.<namespace>.svc.cluster.local
```

Tên trần `kyc` chỉ phân giải được từ bên trong namespace `kalapa`, nhờ search
path trong `/etc/resolv.conf`. Từ `observability` thì không. Cấu hình dùng
`kyc.kalapa` — hai nhãn, không mơ hồ, và đủ ngắn để đọc.

Ba Service của CloudNativePG đáng thuộc tên:

| Service | Trỏ tới |
|---|---|
| `kalapa-db-rw` | primary hiện tại — dùng cái này |
| `kalapa-db-ro` | chỉ replica (rỗng khi `instances: 1`) |
| `kalapa-db-r` | instance bất kỳ |

Trỏ vào `-rw` chính là thứ làm cho failover vô hình với ứng dụng: operator
chuyển hướng Service, và không cấu hình nào phải đổi.

```bash
kubectl -n kalapa run dns --rm -it --image=busybox --restart=Never -- \
  sh -c 'nslookup kyc.kalapa; nslookup kalapa-db-rw.data; cat /etc/resolv.conf'
```

---

## 10. Downward API

```yaml
- name: K8S_POD_NAME
  valueFrom: { fieldRef: { fieldPath: metadata.name } }
```

Các giá trị này trở thành resource attribute trên mọi span và trường trên mọi
dòng log (`internal/platform/obs/tracing.go`). Đó là thứ cho phép bạn lấy một
đợt tăng latency trong Grafana, lọc xuống đúng một pod, và đọc đúng log của
pod đó — khác biệt giữa "service chậm" và "cái pod rơi xuống node X sau đợt
rollout lúc 14:02 thì chậm".

---

## 11. Operator và CRD

Ở đây có hai operator, cả hai theo cùng một mẫu: một CRD mô tả trạng thái mong
muốn, một controller đối chiếu và điều hoà.

| Operator | CRD | Cho bạn |
|---|---|---|
| CloudNativePG | `Cluster` | failover, sao lưu, nâng cấp minor cuốn chiếu |
| Strimzi | `Kafka`, `KafkaNodePool`, `KafkaTopic` | vòng đời broker, quản lý topic |

Ràng buộc thứ tự sinh ra từ đó là thật: apply một `Cluster` trước khi CRD tồn
tại sẽ hỏng với `no matches for kind`. Đó chính là lý do sync wave của Argo CD
tồn tại — operator ở wave `-10`, custom resource của chúng ở wave `10`.

```bash
kubectl get crd | grep -E 'cnpg|strimzi'
kubectl -n data get cluster kalapa-db -o yaml | yq '.status'
kubectl -n kafka get kafka,kafkanodepool,kafkatopic
```

---

## Danh sách tự kiểm

Những thứ nên trả lời được mà không cần tra:

- [ ] Vì sao liveness không được kiểm tra database
- [ ] `maxUnavailable: 0` tốn gì và đổi lại được gì
- [ ] Vì sao `preStop` phải ngủ, và thiếu nó thì hỏng gì
- [ ] `resources: {}` tạo ra QoS class nào, và vì sao điều đó quan trọng
- [ ] Vì sao trong container phải đặt `GOMAXPROCS` tường minh
- [ ] Vì sao đặt cả `replicas` lẫn HPA là một bug
- [ ] Vì sao PDB với `minAvailable: 1` và một replica làm treo lệnh drain
- [ ] Vì sao NetworkPolicy đầu tiên bạn viết sẽ làm hỏng DNS
- [ ] Namespace `kalapa` chạy dưới profile `pod-security` nào, và nó cấm gì
- [ ] Vì sao dùng `kalapa-db-rw` chứ không phải địa chỉ của pod
