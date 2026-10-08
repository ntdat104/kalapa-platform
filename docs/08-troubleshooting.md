# 08 — Xử lý sự cố

Triệu chứng → nguyên nhân khả dĩ → cách sửa. Sắp theo mức độ thường gặp.

Lần nào cũng bắt đầu từ đây:

```bash
make status
```

---

## Pod

### `ImagePullBackOff` / `ErrImagePull`

```bash
kubectl -n kalapa describe pod <pod> | grep -A5 Events
```

| Nguyên nhân | Cách sửa |
|---|---|
| CI chưa từng chạy, nên image trên GHCR chưa tồn tại | `make build` để build thẳng vào Docker daemon của cluster |
| Chart trỏ sai owner | kiểm tra `image.repository` — `scripts/init-repo.sh` sẽ viết lại nó |
| Package GHCR ở chế độ private | chuyển sang public, hoặc thêm `imagePullSecret` |

`make build` chạy được vì `pullPolicy: IfNotPresent` của chart và vì image
được build bên trong daemon của minikube. Nếu bạn build trên máy host thì
cluster không nhìn thấy — phải `eval $(minikube -p kalapa docker-env)` trước.

### `CrashLoopBackOff`

```bash
kubectl -n kalapa logs <pod> --previous     # --previous mới là phần quan trọng:
                                            # container hiện tại chưa có log
```

| Dòng log | Nguyên nhân |
|---|---|
| `postgres not ready after 90s` | DB chưa lên, hoặc DSN sai |
| `kafka producer: ...` | không với tới broker; kiểm tra `kubectl -n kafka get kafka` |
| `permission denied` trên `/app` | `runAsUser` trong chart ≠ `USER` trong Dockerfile |
| `no such file or directory` | mất `CGO_ENABLED=0` — binary liên kết động mà distroless không có libc |
| exit code 137 | OOMKilled — xem bên dưới |

### `OOMKilled` (exit code 137)

```bash
kubectl -n kalapa get pod <pod> -o jsonpath='{.status.containerStatuses[0].lastState}' | jq
kubectl top pod -n kalapa
```

Tăng `resources.limits.memory` **và** `goMemLimit` cùng nhau. Chỉ tăng limit
thì lãng phí khoảng dư; chỉ tăng `GOMEMLIMIT` thì GC vật lộn cho tới sát lằn
ranh kernel giết. Giữ `goMemLimit` thấp hơn limit khoảng 25%.

### `Pending`

```bash
kubectl -n kalapa describe pod <pod> | grep -A10 Events
```

| Thông báo | Nguyên nhân |
|---|---|
| `Insufficient memory` | node đã đầy — xem "Chạy với ít tài nguyên hơn" trong sổ tay vận hành |
| `pod has unbound immediate PersistentVolumeClaims` | `kubectl get pvc -A`; thường là addon storage provisioner đang tắt |
| `node(s) didn't match pod anti-affinity` | bạn yêu cầu 2+ instance của cùng một thứ trên cluster 1 node |

### `ContainerCreating` mãi không xong

Gần như luôn là thiếu ConfigMap hoặc Secret:

```bash
kubectl -n kalapa describe pod <pod> | grep -i 'configmap\|secret'
kubectl -n kalapa get configmap,secret
```

Đó là lý do `kalapa-config` nằm ở sync wave 20 còn các service ở wave 30.

### Deployment tạo được, nhưng không pod nào xuất hiện

```bash
kubectl -n kalapa describe rs -l app.kubernetes.io/name=<svc> | grep -A3 FailedCreate
```

`violates PodSecurity "restricted:latest"` nghĩa là Pod Security Admission đã
từ chối pod template. Deployment thì được chấp nhận (nó chỉ nhận *cảnh báo*);
Pod thì bị từ chối. Mọi chart bên thứ ba bạn cài vào `kalapa` đều phải thoả
`restricted` — xem `deploy/argocd/platform/03-reloader.yaml` để biết điều đó
trông thế nào trong thực tế.

---

## Mạng

### Ingress trả 502 trong khi pod vẫn khoẻ

```bash
kubectl -n ingress-nginx logs deploy/ingress-nginx-controller --tail=50
kubectl -n kalapa get endpoints gateway      # có rỗng không?
```

Endpoint rỗng = không pod nào qua được readiness. Con 502 là cách Ingress nói
với bạn rằng nó không có chỗ nào để gửi request tới.

### 503 lác đác trong lúc deploy

Khoảng dừng `preStop` quá ngắn hoặc không có. Xem
[02-kubernetes-deep-dive.md](02-kubernetes-deep-dive.md#3-tắt-mềm-năm-thứ-phải-khớp-nhau)
và lab 2.

### `api.kalapa.local` không phân giải được

```bash
make hosts                                  # chạy lại sau mỗi lần dựng lại cluster
grep kalapa /etc/hosts
minikube ip -p kalapa                       # phải khớp
```

IP đổi khi cluster được dựng lại. `scripts/hosts.sh` xoá khối cũ trước khi ghi
khối mới, nên chạy lại là an toàn.

### Gọi giữa service với nhau hỏng với `no such host`

```bash
kubectl -n kalapa run dns --rm -it --image=busybox --restart=Never -- \
  nslookup kyc.kalapa
```

Tên trần `kyc` chỉ phân giải được từ bên trong namespace `kalapa`. Hãy dùng
`<service>.<namespace>`. Nếu cả cái đó cũng hỏng, kiểm tra xem có NetworkPolicy
nào đang chặn egress tới `kube-dns` không — đó là lỗi kinh điển của policy đầu
tiên.

---

## Argo CD

### Application kẹt ở `OutOfSync` mà không thấy khác biệt nào

Có một controller đang ghi ngược các trường vào tài nguyên. Hãy thêm khối
`ignoreDifferences` — `deploy/argocd/platform/21-kafka.yaml` có sẵn mẫu.

```bash
kubectl -n argocd get application <name> -o yaml | yq '.status.conditions'
```

### `ComparisonError` / `rpc error`

repo-server không render được chart.

```bash
kubectl -n argocd logs deploy/argocd-repo-server --tail=100
```

| Log | Nguyên nhân |
|---|---|
| `found in Chart.yaml, but missing in charts/ directory` | dependency `file://` chưa được build; chạy `helm dependency build` ở máy rồi commit file lock |
| repo-server bị `OOMKilled` | render kube-prometheus-stack cần ~384 Mi; tăng `repoServer.resources.limits.memory` |
| `authentication required` | repo riêng tư mà chưa có repository Secret — xem [04](04-gitops-with-argocd.md#repo-riêng-tư) |

### Application đứng ở `Missing` / không có gì xảy ra

```bash
kubectl -n argocd get application <name> -o jsonpath='{.spec.source.repoURL}'
```

Nếu nó in ra `__GIT_REPO_URL__` thì chạy `scripts/init-repo.sh` rồi push.

### `selfHeal` cứ hoàn nguyên thay đổi của tôi

Nó đang hoạt động đúng thiết kế. Hãy đổi trong Git, hoặc tạm thời:

```bash
kubectl -n argocd patch application <name> --type merge \
  -p '{"spec":{"syncPolicy":{"automated":null}}}'
```

Nhớ bật lại.

---

## Tầng dữ liệu

### Cluster CloudNativePG không sẵn sàng

```bash
kubectl -n data get cluster kalapa-db -o yaml | yq '.status'
kubectl -n data logs kalapa-db-1 --tail=50
kubectl -n data get events --sort-by=.lastTimestamp | tail -20
```

| Nguyên nhân | Cách sửa |
|---|---|
| PVC đang Pending | `kubectl get pvc -n data`; kiểm tra storage provisioner |
| thiếu Secret thông tin đăng nhập | nó nằm cùng chart; kiểm tra thứ tự đồng bộ |
| Secret không phải loại `kubernetes.io/basic-auth` | operator đòi đúng loại đó và các key `username`/`password` |

### Kafka không bao giờ Ready

```bash
kubectl -n kafka get kafka,kafkanodepool
kubectl -n kafka logs deploy/strimzi-cluster-operator --tail=100
kubectl -n kafka logs kalapa-combined-0 --tail=50
```

| Nguyên nhân | Cách sửa |
|---|---|
| replication factor bằng 3 trên một broker | mọi `*.replication.factor` phải là 1 — xem ghi chú trong `deploy/charts/kafka/templates/kafka.yaml` |
| phiên bản Kafka không được hỗ trợ | `kubectl -n kafka get deploy strimzi-cluster-operator -o yaml \| grep -A3 STRIMZI_KAFKA_IMAGES` liệt kê những bản operator chấp nhận |
| không có `KafkaNodePool` | Strimzi 1.x bắt buộc phải có; một `Kafka` đơn độc sẽ không bao giờ ready |
| operator không theo dõi namespace đó | `watchNamespaces` trong `02-strimzi-operator.yaml` |

Lưu ý thêm: nếu lỗi là `no matches for kind "Kafka"` trong khi CRD rõ ràng đã
cài, thì gần như chắc chắn bạn đang dùng sai apiVersion. Strimzi 1.x phục vụ
`kafka.strimzi.io/v1`, không phải `v1beta2` như YAS và phần lớn hướng dẫn cũ.
Kiểm tra bằng:

```bash
kubectl api-resources --api-group=kafka.strimzi.io
```

### Keycloak không bao giờ ready

Các endpoint health của Keycloak nằm trên **cổng management (9000)** kể từ
v25. Dò cổng 8080 sẽ trả 404 mãi mãi. Lần khởi động đầu còn chạy toàn bộ
migration schema trên một database rỗng — mất vài phút trên laptop, đó là lý
do `startupProbe.failureThreshold` là 60.

```bash
kubectl -n identity logs deploy/keycloak --tail=100
kubectl -n identity get cluster keycloak-db
```

---

## Observability

### Prometheus không có metric của một service

Theo thứ tự:

```bash
kubectl -n kalapa get servicemonitor                         # có tồn tại không?
kubectl -n observability get prometheus -o yaml | grep -A5 serviceMonitorSelector
kubectl -n observability port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090
# -> http://localhost:9090/targets
kubectl -n kalapa port-forward deploy/kyc 9090:9090
curl -s localhost:9090/metrics | head                         # endpoint có trả lời không?
```

Thường gặp nhất: label `release:` trên ServiceMonitor không khớp với thứ mà
selector của CR Prometheus đang tìm.

### Loki không có log

```bash
kubectl -n observability logs daemonset/alloy-logs --tail=50
kubectl -n observability port-forward svc/loki-gateway 3100:80
curl -s 'http://localhost:3100/loki/api/v1/labels' | jq
```

| Nguyên nhân | Cách sửa |
|---|---|
| Alloy không đọc được `/var/log/pods` | `alloy.mounts.varlog: true` |
| thiếu `local.file_match` | `loki.source.file` không tự khai triển glob; triệu chứng là lỗi `stat failed` cho từng container và không dòng log nào tới nơi |
| container runtime là docker | file dưới `/var/log/pods` chỉ là symlink vào `/var/lib/docker/containers`; cần thêm `alloy.mounts.dockercontainers: true` |
| có log nhưng mất label `level`/`service` | sai stage giải mã: `stage.cri` cho containerd, `stage.docker` cho docker — cấu hình ở đây liệt kê cả hai |
| log bị từ chối vì quá cũ | lệch đồng hồ; `reject_old_samples_max_age` |

### Tempo không có trace

```bash
kubectl -n observability logs deploy/opentelemetry-collector --tail=50
kubectl -n observability port-forward svc/opentelemetry-collector 8888:8888
curl -s localhost:8888/metrics | grep -E 'refused|dropped|failed'
```

| Nguyên nhân | Cách sửa |
|---|---|
| sai cổng OTLP | 4317 là gRPC, 4318 là HTTP; nhầm hai cái sẽ kết nối được rồi âm thầm rơi hết span |
| `tracing.enabled: false` | kiểm tra `/etc/kalapa/config.yaml` đang được mount |
| `sampleRatio: 0` | không gì được lấy mẫu |
| span mất lúc tắt | exporter chưa được flush — xem `cmd/*/main.go` |
| truy vấn Tempo trả "no data" | cổng API truy vấn của Tempo là **3200**, không phải 3100 (3100 là của Loki) |

### Trace dừng lại ở ranh giới Kafka

`RecordCarrier` chưa được nối vào. Producer phải `Inject` vào header của
record và consumer phải `Extract` trước khi bắt đầu span của nó — xem
[03-observability.md](03-observability.md#3-traces).

### Service map của Grafana trống

metrics-generator của Tempo cần **cả hai**:

```yaml
metricsGenerator: { enabled: true, remoteWriteUrl: ... }
overrides:
  defaults:
    metrics_generator:
      processors: [service-graphs, span-metrics]   # cái này dễ bị bỏ sót
```

và `serviceMap.datasourceUid` của Tempo datasource phải trỏ tới Prometheus.

---

## Toàn cluster

### Mọi thứ đều chậm, pod bị evict

```bash
kubectl top nodes
kubectl describe node kalapa | grep -A8 'Allocated resources'
kubectl get events -A --field-selector reason=Evicted
```

Node hết bộ nhớ. Xem
[06-runbook.md](06-runbook.md#chạy-với-ít-tài-nguyên-hơn) để biết nên tắt gì
trước.

### `kubectl top` báo lỗi

```bash
minikube addons enable metrics-server -p kalapa
kubectl -n kube-system rollout status deploy/metrics-server
```

HPA cũng phụ thuộc vào cái này — thiếu nó thì HPA hiển thị `<unknown>/70%` mãi
mãi.

### Làm lại từ đầu

```bash
make down              # xoá cluster và toàn bộ volume
make cluster bootstrap # hoặc: make cluster && make up-local
```

Không mất gì ngoài những thứ không nằm trong Git — và đó chính là mục đích.
