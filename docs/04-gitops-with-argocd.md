# 04 — GitOps với Argo CD

## GitOps thực sự tuyên bố điều gì

> Trạng thái mong muốn của cluster nằm trong Git. Một controller bên trong
> cluster liên tục kéo trạng thái đó về và điều chỉnh thực tế cho khớp.

Bốn hệ quả, và đó là lý do để bận tâm:

1. **Trạng thái cluster xem xét được.** Nó là một diff trên pull request.
2. **Drift bị phát hiện.** Một lệnh `kubectl edit` chạy tay sẽ hiện ra là
   `OutOfSync` và, với `selfHeal`, bị hoàn nguyên.
3. **Rollback là `git revert`.** Không phải một câu lệnh phải nhớ và một lời
   cầu nguyện.
4. **CI không cần thông tin đăng nhập cluster.** Pipeline đẩy một commit; nó
   không bao giờ chạm vào API server. Một workflow bị chiếm quyền cũng không
   với tới cluster được.

Điểm 4 thường bị đánh giá thấp. Trong pipeline kiểu đẩy, mọi CI runner đều giữ
một kubeconfig. Trong pipeline kiểu kéo, thứ duy nhất có quyền với cluster là
cái controller vốn đã nằm sẵn bên trong cluster.

---

## Cấu trúc ở đây

```
deploy/argocd/
├── bootstrap/
│   ├── argocd-values.yaml     cách cài chính Argo CD (Helm, chạy tay)
│   └── root-app.yaml          ĐÚNG MỘT object bạn apply thủ công
├── projects/
│   ├── platform.yaml          AppProject: được tạo object phạm vi cluster
│   └── kalapa.yaml            AppProject: không được
├── platform/                  Application cho hạ tầng, sắp theo sync wave
│   ├── 00-namespaces.yaml         wave -20
│   ├── 01-cnpg-operator.yaml      wave -10
│   ├── 02-strimzi-operator.yaml   wave -10
│   ├── 03-reloader.yaml           wave -10
│   ├── 10-kube-prometheus-stack   wave 0
│   ├── 11-loki / 12-tempo         wave 0
│   ├── 13-otel-collector          wave 1
│   ├── 14-alloy-logs              wave 1
│   ├── 15-grafana-config          wave 2
│   └── 20-postgres / 21-kafka / 22-keycloak   wave 10
└── apps/
    ├── 00-kalapa-config.yaml      wave 20
    └── 10-services.yaml           ApplicationSet -> wave 30
```

### App of apps

Một `Application` (`root`) có nguồn là *một thư mục chứa các Application
manifest*. Argo CD đồng bộ nó, việc đó tạo ra các Application con, rồi chúng
tự đồng bộ chính mình.

```yaml
source:
  path: deploy/argocd
  directory:
    recurse: true
    include: '{projects/*.yaml,platform/*.yaml,apps/*.yaml}'
```

Bộ lọc `include` là quan trọng: không có nó, việc quét đệ quy sẽ nhặt luôn các
Helm chart nằm cạnh mấy file này và cố apply template thô của chúng — vốn
không phải YAML hợp lệ trước khi được render.

Thêm một thành phần giờ chỉ là: viết Application manifest, commit. Không phải
sửa script, không có gì phải nhớ chạy.

### Sync wave

```yaml
annotations:
  argocd.argoproj.io/sync-wave: "-10"
```

Số nhỏ chạy trước. Argo CD chờ một wave đạt **Healthy** rồi mới bắt đầu wave
kế — một cổng chờ thật sự, khác với `sleep 60` của YAS.

| Wave | Cái gì | Vì sao ở đó |
|---|---|---|
| -20 | namespace | mọi thứ khác đều thuộc namespace |
| -10 | CNPG, Strimzi, Reloader | CRD phải tồn tại trước custom resource của chúng |
| 0 | Prometheus, Loki, Tempo | nơi nhận telemetry |
| 1 | OTel Collector, Alloy | chúng báo lỗi cho tới khi nơi nhận tồn tại |
| 2 | datasource/dashboard của Grafana | sidecar phải đang chạy mới nhận ra chúng |
| 10 | Postgres, Kafka, Keycloak | cần CRD ở wave -10 |
| 20 | kalapa-config | pod thiếu ConfigMap sẽ treo ở ContainerCreating |
| 30 | gateway, kyc, scoring | cần config, database và broker |

Sync wave sắp thứ tự các tài nguyên *trong một thao tác đồng bộ*. Lần đồng bộ
của root app chính là thao tác đó, và các Application con là tài nguyên của
nó — đó là lý do annotation nằm trên các object `Application` chứ không nằm
bên trong chart.

### AppProject

Không có nó, mọi Application chạy dưới `default`, vốn cho phép mọi repo nguồn,
mọi cluster đích, mọi loại tài nguyên. Một Git repo bị chiếm quyền khi đó
tương đương cluster-admin.

```yaml
# kalapa.yaml — project của ứng dụng
sourceRepos: [__chỉ repo của bạn__]
destinations: [{ namespace: kalapa, server: ... }]
clusterResourceWhitelist: []     # không object phạm vi cluster nào hết
namespaceResourceWhitelist:      # và một danh sách loại tài nguyên tường minh
  - { group: apps, kind: Deployment }
  ...
```

Nếu một chart service mọc thêm một `ClusterRole`, lần đồng bộ sẽ bị **từ chối**
thay vì âm thầm cấp quyền. Đó là lúc ranh giới làm đúng việc của nó.

```bash
kubectl -n argocd get appproject -o yaml | yq '.items[].spec.sourceRepos'
```

### ApplicationSet

`apps/10-services.yaml` sinh ra một Application cho mỗi thư mục chart service:

```yaml
generators:
  - git:
      directories:
        - path: deploy/charts/*
        - path: deploy/charts/go-service
          exclude: true          # là dependency, không phải thứ để deploy
        ...
template:
  metadata: { name: '{{path.basename}}' }
```

Ba Application gần như giống hệt nhau thì hôm nay cũng chạy tốt. Dùng bộ sinh
nghĩa là thêm service thứ tư chỉ còn là "tạo một thư mục chart" — chứ không
phải "tạo một thư mục chart rồi nhớ viết thêm một Application và đặt đúng
project, sync policy và wave cho nó".

---

## Sync policy, từng tuỳ chọn

```yaml
syncPolicy:
  automated:
    prune: true
    selfHeal: true
  syncOptions:
    - CreateNamespace=true
    - ServerSideApply=true
  retry:
    limit: 5
    backoff: { duration: 10s, factor: 2, maxDuration: 3m }
```

| Tuỳ chọn | Không có nó thì |
|---|---|
| `prune: true` | xoá một file khỏi Git nhưng object vẫn chạy mãi; Git thôi là nguồn sự thật |
| `selfHeal: true` | một lệnh `kubectl edit` dính luôn tới commit kế tiếp; drift được báo nhưng không được sửa |
| `CreateNamespace=true` | lần đồng bộ đầu hỏng với "namespace not found" |
| `ServerSideApply=true` | CRD lớn vượt giới hạn 256 KB của annotation `last-applied-configuration` |
| `retry` | một phụ thuộc chậm 20 giây làm Application mắc kẹt ở `Degraded` vĩnh viễn |

**Hãy chuẩn bị tinh thần rằng `selfHeal` sẽ phá việc debug của bạn.**
`kubectl scale deploy/kyc --replicas=3` bị hoàn nguyên trong vòng một chu kỳ
điều hoà. Đó là hành vi đúng và nó gây mất phương hướng trong vài lần đầu. Để
thử nghiệm, hoặc tắt auto-sync trên Application đó, hoặc đổi con số trong Git.

---

## `ignoreDifferences`: thiết lập chấm dứt các cuộc chiến

Controller ghi vào chính các object chúng sở hữu. Argo CD đọc các lần ghi đó
là drift, hoàn nguyên chúng, rồi controller ghi lại — lặp vô tận.

```yaml
# kube-prometheus-stack: một Job tiêm CA bundle của webhook lúc chạy
ignoreDifferences:
  - group: admissionregistration.k8s.io
    kind: ValidatingWebhookConfiguration
    jsonPointers: [/webhooks/0/clientConfig/caBundle]
```

Thiếu nó, Argo CD xoá CA bundle mỗi hai phút và admission webhook hỏng theo
chu kỳ.

```yaml
# Strimzi tự điền giá trị mặc định cho một loạt trường trên Kafka resource
  - group: kafka.strimzi.io
    kind: Kafka
    jqPathExpressions: ['.spec.kafka.config']
```

Thiếu nó, Application Kafka không bao giờ rời `OutOfSync` kể cả khi nó hoàn
toàn đúng. Triệu chứng — `OutOfSync` vĩnh viễn, mà diff trên giao diện không
cho thấy khác biệt thật nào — đủ đặc trưng để nhận ra.

---

## Tag image, và vì sao `:latest` phá vỡ toàn bộ mô hình

```yaml
image:
  tag: latest        # <- dòng này vô hiệu hoá GitOps
```

Argo CD so manifest trong Git với manifest trong cluster. Cả hai ghi `:latest`.
Nó báo `Synced`. Nó không có cách nào biết `:latest` giờ trỏ tới một digest
khác, nên cluster có thể đang chạy code cũ tuỳ ý với dashboard màu xanh.

Cách sửa là để CI ghi một tag bất biến vào `values.yaml`:

```yaml
image:
  tag: a3f2c91b4e07     # short SHA của commit đã build ra nó
```

Giờ `Synced` có nghĩa, phiên bản đang chạy nằm trong `git log`, và rollback là
`git revert`. [05-ci-cd.md](05-ci-cd.md) có workflow đó.

---

## Secret

Repo này commit mật khẩu dev dưới dạng chữ rõ. Điều đó chỉ bào chữa được **vì**
chúng là `kalapa-dev-password` trên một chiếc laptop, và vì giấu chúng đi sẽ
giấu luôn cái luồng mà bạn đang đến đây để học. Với bất cứ thứ gì thật, chọn
một trong ba:

| Cách | Hoạt động thế nào | Đánh đổi |
|---|---|---|
| **Sealed Secrets** | mã hoá bằng khoá công khai riêng của cluster; commit bản mã | đơn giản; khoá gắn với một cluster, nên khôi phục thảm hoạ cần sao lưu khoá |
| **External Secrets** | commit một *tham chiếu*; operator kéo từ Vault/AWS SM | không có bản mã nào trong Git; thêm một operator và một phụ thuộc bên ngoài |
| **SOPS + age** | mã hoá ngay trong repo; giải mã lúc apply | trải nghiệm lập trình tốt; Argo CD cần plugin hoặc `kustomize-sops` |

Lab 7 chuyển ba Secret của repo này sang Sealed Secrets.

Dù chọn cách nào, quy tắc không đổi: **đừng bao giờ `kubectl create secret`
bằng tay.** Một secret chỉ tồn tại trong cluster là trạng thái không ai tái
tạo được, và lần dựng lại cluster đầu tiên sẽ làm mất nó.

---

## Repo riêng tư

Script bootstrap mặc định dùng repo công khai để Argo CD không cần thông tin
đăng nhập. Với repo riêng tư:

```bash
kubectl -n argocd create secret generic repo-kalapa \
  --from-literal=type=git \
  --from-literal=url=https://github.com/<owner>/kalapa-platform \
  --from-literal=username=<owner> \
  --from-literal=password=<GitHub PAT có quyền repo:read>
kubectl -n argocd label secret repo-kalapa argocd.argoproj.io/secret-type=repository
```

Cái label mới là thứ làm Argo CD để ý tới nó. Tốt hơn nữa là dùng GitHub App
hoặc deploy key thay vì PAT, để thông tin đăng nhập chỉ giới hạn trong đúng
một repo.

---

## Vận hành

```bash
# Cluster đang nghĩ gì về chính nó?
kubectl -n argocd get applications

# Vì sao cái này không vui?
kubectl -n argocd describe application kyc | sed -n '/Status:/,$p'

# Chính xác thì khác Git ở chỗ nào?
argocd app diff kyc                 # cần CLI và đã đăng nhập
kubectl -n argocd get application kyc -o yaml | yq '.status.resources'

# Ép đọc lại Git ngay, không chờ hết 2 phút polling
kubectl -n argocd patch application kyc --type merge \
  -p '{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}'

# Nơi việc render chart thất bại — đây là log cần đọc khi gặp "ComparisonError"
kubectl -n argocd logs deploy/argocd-repo-server --tail=100

# Nơi quyết định sync và health
kubectl -n argocd logs statefulset/argocd-application-controller --tail=100
```

### Webhook

Argo CD poll Git mỗi 2 phút (`timeout.reconciliation: 120s`). Một webhook từ
GitHub tới `/api/webhook` làm nó phản ứng trong vài giây và bỏ được việc gọi
API GitHub liên tục. Ở đây chưa cấu hình vì cluster lab không truy cập được từ
Internet; trên cluster thật thì đây là thứ nên thêm đầu tiên.

---

## Danh sách tự kiểm

- [ ] Vì sao việc CI không giữ kubeconfig là một tính chất bảo mật, không phải
      sự tiện lợi
- [ ] `prune: false` âm thầm lấy đi của bạn điều gì
- [ ] `selfHeal: true` sẽ làm gì với lệnh `kubectl scale` của bạn
- [ ] Vì sao operator nằm ở sync wave thấp hơn custom resource của nó
- [ ] Một AppProject với `clusterResourceWhitelist` rỗng ngăn chặn điều gì
- [ ] Vì sao `:latest` làm chữ "Synced" trở nên vô nghĩa
- [ ] `ignoreDifferences` sửa triệu chứng nào, và nhận ra nó bằng cách nào
- [ ] Đọc log nào khi một Application báo `ComparisonError`
