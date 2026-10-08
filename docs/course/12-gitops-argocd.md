# 12 — GitOps với Argo CD

> **Bài này trả lời:** Ai được quyền chạm vào production? Làm sao biết cluster
> đang chạy đúng thứ bạn nghĩ? Rollback kiểu gì?
>
> **Cần xong bài:** [11](11-observability.md)

---

## 1. Lý thuyết

### 1.1 Vấn đề với deploy kiểu đẩy

Cách truyền thống: CI build xong rồi **đẩy** vào cluster.

```
CI runner ──kubectl apply──► cluster
```

Bốn vấn đề, lớn dần:

**1. CI phải giữ chìa khoá cluster.** Mỗi runner có một kubeconfig với quyền
ghi lên production. Một workflow bị chiếm quyền là cluster bị chiếm quyền.

**2. Không ai biết cluster đang chạy gì.** Ai đó chạy `kubectl edit` lúc 2 giờ
sáng để chữa cháy. Sáu tháng sau không ai nhớ. Thứ trong Git không còn là thứ
đang chạy.

**3. Drift vô hình.** Không có cơ chế nào phát hiện khác biệt giữa "đáng lẽ
phải thế" và "đang thế".

**4. Rollback là trò trí nhớ.** Phiên bản trước là gì? Values lúc đó ra sao?

### 1.2 GitOps

Lật ngược chiều:

```
Git  ◄──pull──  Argo CD (chạy TRONG cluster)  ──apply──►  cluster
```

Bốn nguyên tắc:

1. **Trạng thái mong muốn được khai báo** — toàn bộ nằm trong Git dưới dạng YAML
2. **Có phiên bản và bất biến** — Git cho bạn lịch sử, tác giả, diff
3. **Được kéo tự động** — một agent trong cluster tự lấy về
4. **Liên tục điều hoà** — agent so sánh và sửa khác biệt, mãi mãi

Đây **chính là vòng lặp điều hoà ở bài 03**, nâng lên một tầng: thay vì
"ReplicaSet điều hoà Pod", giờ là "Argo CD điều hoà toàn bộ cluster theo Git".

Bốn vấn đề ở trên biến mất:

| Vấn đề | GitOps |
|---|---|
| CI giữ chìa khoá | CI chỉ đẩy commit. Nó **không có** kubeconfig |
| Không biết đang chạy gì | `git log` là lịch sử deploy, có tác giả và thời gian |
| Drift vô hình | Argo CD báo `OutOfSync`, và `selfHeal` tự sửa |
| Rollback bằng trí nhớ | `git revert` |

### 1.3 Argo CD hoạt động thế nào

Thành phần:

| Thành phần | Việc |
|---|---|
| `repo-server` | Clone Git, chạy `helm template`/`kustomize build` → YAML |
| `application-controller` | So YAML đó với cluster, apply phần khác |
| `server` | API và giao diện web |
| `applicationset-controller` | Sinh Application từ mẫu |
| `redis` | Cache kết quả render |

Object trung tâm là **Application**:

```yaml
kind: Application
spec:
  source:
    repoURL: https://github.com/you/repo.git
    targetRevision: main
    path: deploy/charts/kyc
  destination:
    server: https://kubernetes.default.svc
    namespace: kalapa
  syncPolicy:
    automated: { prune: true, selfHeal: true }
```

Dịch ra: "lấy `deploy/charts/kyc` từ nhánh `main`, render nó, và làm cho
namespace `kalapa` khớp — mãi mãi".

### 1.4 App of apps

Nếu mỗi thành phần cần một Application, thì ai tạo các Application đó?

Mẹo: một Application mà **nguồn của nó là một thư mục chứa các Application
khác**.

```
root Application
   └── path: deploy/argocd/   (thư mục chứa các file Application)
         ├── platform/01-cnpg-operator.yaml
         ├── platform/02-strimzi-operator.yaml
         └── apps/10-services.yaml
```

Bạn apply **đúng một** object bằng tay. Nó tạo các Application con, chúng tự
đồng bộ. Thêm thành phần mới = thêm một file YAML, commit.

### 1.5 Sync wave

Thứ tự quan trọng: operator phải có trước custom resource, ConfigMap phải có
trước pod mount nó.

```yaml
annotations:
  argocd.argoproj.io/sync-wave: "-10"
```

Số nhỏ chạy trước. Argo CD **chờ một wave đạt Healthy** rồi mới sang wave sau.

> So với `sleep 60` trong script: `sleep` quá dài khi nhanh và quá ngắn khi
> chậm. Sync wave chờ đúng điều kiện thật.

### 1.6 prune và selfHeal

| Tuỳ chọn | Bật | Tắt |
|---|---|---|
| `prune` | Xoá file khỏi Git → object bị xoá khỏi cluster | Object chạy mãi, Git thôi là nguồn sự thật |
| `selfHeal` | `kubectl edit` bị hoàn nguyên trong ~2 phút | Drift được báo nhưng không sửa |

**Hãy chuẩn bị tinh thần:** `selfHeal` sẽ phá việc debug của bạn. Bạn
`kubectl scale`, hai phút sau nó quay về. Đó là hành vi đúng và nó gây mất
phương hướng vài lần đầu.

### 1.7 ignoreDifferences

Có những trường do **controller khác** ghi, không phải bạn. Ví dụ một Job tiêm
CA bundle vào webhook. Argo CD đọc đó là drift, hoàn nguyên, controller ghi
lại — lặp vô tận, và webhook hỏng theo chu kỳ.

```yaml
ignoreDifferences:
  - group: admissionregistration.k8s.io
    kind: ValidatingWebhookConfiguration
    jsonPointers: [/webhooks/0/clientConfig/caBundle]
```

> Triệu chứng cần nhận ra: Application **kẹt `OutOfSync` vĩnh viễn** nhưng
> diff trên giao diện không cho thấy khác biệt thật nào.

### 1.8 Vì sao `:latest` phá vỡ toàn bộ mô hình

Đây là điểm quan trọng nhất của bài.

```yaml
image:
  tag: latest
```

Argo CD so manifest trong Git với manifest trong cluster. **Cả hai đều ghi
`:latest`.** Nó báo `Synced`. Và nó **không có cách nào biết** rằng `:latest`
giờ trỏ tới một digest khác.

Hệ quả: cluster có thể đang chạy code cũ tuỳ ý, với dashboard màu xanh.

Cách sửa: CI ghi một **tag bất biến** vào `values.yaml`:

```yaml
image:
  tag: a3f2c91b4e07     # short SHA của commit đã build ra nó
```

Giờ `Synced` mới có nghĩa, phiên bản đang chạy nằm trong `git log`, và rollback
là `git revert`.

### 1.9 AppProject — ranh giới phân quyền

Không có AppProject, mọi Application chạy dưới `default`, cho phép **mọi repo
nguồn, mọi cluster đích, mọi loại tài nguyên**. Một Git repo bị chiếm quyền khi
đó tương đương cluster-admin.

AppProject giới hạn: repo nào được dùng làm nguồn, namespace nào được ghi, và
loại tài nguyên nào được tạo.

---

## 2. Trong repo này nằm đâu

### Cấu trúc

```
deploy/argocd/
├── bootstrap/
│   ├── argocd-values.yaml     cách cài Argo CD (Helm, bằng tay)
│   └── root-app.yaml          ĐÚNG MỘT object bạn apply thủ công
├── projects/                  ranh giới phân quyền
├── platform/                  Application cho hạ tầng, có sync wave
└── apps/                      Application cho service Kalapa
```

### Root application

`deploy/argocd/bootstrap/root-app.yaml:9`

```yaml
kind: Application
metadata:
  name: root
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  source:
    repoURL: __GIT_REPO_URL__
    path: deploy/argocd
    directory:
      recurse: true
      include: '{projects/*.yaml,platform/*.yaml,apps/*.yaml}'
```

Hai chi tiết:

**`finalizers`** — thiếu nó, xoá root Application sẽ bỏ rơi mọi con: chúng
biến mất khỏi Argo CD trong khi workload vẫn chạy.

**`include`** — thiếu nó, việc quét đệ quy sẽ nhặt luôn các Helm chart nằm cạnh
và cố apply template thô, vốn không phải YAML hợp lệ.

### Sync policy

Cùng file, dòng 32–39:

```yaml
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ApplyOutOfSyncOnly=true
    retry:
      limit: 5
      backoff: { duration: 10s, factor: 2, maxDuration: 3m }
```

### Sync wave trong thực tế

| File | Wave | Vì sao ở đó |
|---|---|---|
| `deploy/argocd/platform/00-namespaces.yaml` | -20 | mọi thứ khác đều thuộc namespace |
| `deploy/argocd/platform/01-cnpg-operator.yaml` | -10 | CRD phải có trước custom resource |
| `deploy/argocd/platform/02-strimzi-operator.yaml` | -10 | như trên |
| `deploy/argocd/platform/10-kube-prometheus-stack.yaml` | 0 | nơi nhận telemetry |
| `deploy/argocd/platform/13-otel-collector.yaml` | 1 | báo lỗi nếu nơi nhận chưa có |
| `deploy/argocd/platform/20-postgres.yaml` | 10 | cần CRD ở wave -10 |
| `deploy/argocd/apps/00-kalapa-config.yaml` | 20 | pod thiếu ConfigMap sẽ treo |
| `deploy/argocd/apps/10-services.yaml` | 30 | cần config, DB, broker |

### ignoreDifferences thật

`deploy/argocd/platform/21-kafka.yaml`:

```yaml
  ignoreDifferences:
    - group: kafka.strimzi.io
      kind: Kafka
      jqPathExpressions:
        - '.spec.kafka.config'
```

Strimzi tự điền mặc định cho một loạt trường. Thiếu khối này thì Application
Kafka **không bao giờ rời `OutOfSync`** dù hoàn toàn đúng.

### ApplicationSet

`deploy/argocd/apps/10-services.yaml:11`

```yaml
kind: ApplicationSet
spec:
  generators:
    - git:
        directories:
          - path: deploy/charts/*
          - path: deploy/charts/go-service
            exclude: true          # là dependency, không phải thứ để deploy
          - path: deploy/charts/kalapa-config
            exclude: true
  template:
    metadata:
      name: '{{path.basename}}'
```

Nó quét repo và sinh một Application cho mỗi thư mục chart service.

> Ba Application viết tay cũng chạy tốt hôm nay. Dùng generator nghĩa là thêm
> service thứ tư chỉ còn là "tạo thư mục chart" — chứ không phải "tạo thư mục
> chart rồi nhớ viết Application và đặt đúng project, sync policy, wave".

### AppProject

`deploy/argocd/projects/kalapa.yaml:17`

```yaml
  clusterResourceWhitelist: []     # KHÔNG object phạm vi cluster nào
  namespaceResourceWhitelist:
    - { group: '', kind: Service }
    - { group: apps, kind: Deployment }
    # ...
```

Nếu một chart service mọc thêm `ClusterRole`, lần đồng bộ bị **từ chối** thay
vì âm thầm cấp quyền.

### Cấu hình Argo CD

`deploy/argocd/bootstrap/argocd-values.yaml`. Đáng đọc nhất:

```yaml
  params:
    server.insecure: true
```

> Để TLS bật thì Argo CD chuyển hướng sang HTTPS, Ingress chuyển tiếp HTTP, và
> bạn có vòng lặp chuyển hướng vô tận. Đây là lỗi phổ biến nhất khi đặt Argo CD
> sau ingress.

Và:

```yaml
repoServer:
  resources:
    limits:
      memory: 384Mi
```

> Render kube-prometheus-stack (vài nghìn manifest) là đỉnh tiêu thụ. Dưới
> ~384Mi thì repo-server bị OOMKill giữa chừng và Application báo một lỗi
> `rpc error: code = Unavailable` rất khó hiểu.

### Script bootstrap

`scripts/01-bootstrap-argocd.sh` — để ý dòng này:

```bash
kube apply -f "$ROOT/deploy/argocd/bootstrap/root-app.yaml"
```

Đây là **lệnh `kubectl apply` duy nhất trong toàn bộ quy trình**. Từ đó trở đi
cluster tự kéo trạng thái về từ Git.

---

## 3. Thực hành

### 3.1 Nếu chưa cài Argo CD

```bash
cd ~/Documents/resource/kalapa-platform
gh auth login
make init
git add -A && git commit -m "wire repo url" && git push -u origin main
make bootstrap
```

Nếu không muốn dùng GitHub, bỏ qua phần thực hành của bài này và đọc lại mục
1 — hoặc dùng `make up-local` để thấy phần còn lại vẫn chạy mà không có GitOps.

### 3.2 Nhìn vào các Application

```bash
kubectl -n argocd get applications
kubectl -n argocd get applications -o custom-columns=\
NAME:.metadata.name,WAVE:'.metadata.annotations.argocd\.argoproj\.io/sync-wave',SYNC:.status.sync.status,HEALTH:.status.health.status
```

Sắp xếp theo wave và bạn thấy đúng thứ tự khởi tạo.

### 3.3 Giao diện

```bash
make argocd-password
make pf-argocd      # localhost:8080
```

Trong giao diện: bấm vào `root` để thấy cây app-of-apps; bấm vào `kyc` để thấy
cây tài nguyên và nút **App Diff**.

### 3.4 Chứng kiến selfHeal — bài quan trọng nhất

```bash
# Terminal 1
kubectl -n kalapa get deploy kyc -w

# Terminal 2 — thay đổi ngoài luồng
kubectl -n kalapa scale deploy/kyc --replicas=3
kubectl -n argocd get application kyc -o jsonpath='{.status.sync.status}'; echo
```

Ở terminal 1 bạn thấy 3 pod. Trong vòng ~2 phút, Argo CD kéo về 1.

Xem nó phát hiện drift:

```bash
kubectl -n argocd get application kyc -o yaml | yq '.status.resources[] | select(.status != "Synced")'
```

Muốn scale thật thì **đổi trong Git**:

```bash
# sửa deploy/charts/kyc/values.yaml -> go-service.replicaCount: 2
git commit -am "scale kyc len 2" && git push
# ép đọc lại Git ngay, không chờ 2 phút
kubectl -n argocd patch application kyc --type merge \
  -p '{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}'
```

### 3.5 Chứng kiến prune

```bash
# Thêm một object vào chart
cat >> deploy/charts/kyc/values.yaml <<'EOF'
  extraObjects:
    - apiVersion: v1
      kind: ConfigMap
      metadata:
        name: thu-prune
      data:
        ghi-chu: "object nay se bi prune"
EOF
git commit -am "them configmap thu" && git push
# chờ đồng bộ
kubectl -n kalapa get cm thu-prune
```

Giờ xoá khỏi Git:

```bash
git revert --no-edit HEAD && git push
# chờ đồng bộ
kubectl -n kalapa get cm thu-prune     # -> NotFound
```

> `prune: false` thì ConfigMap đó sẽ **ở lại mãi mãi**, và Git thôi là nguồn
> sự thật.

### 3.6 Rollback kiểu GitOps

```bash
git log --oneline -5 -- deploy/charts/kyc/values.yaml
git revert --no-edit <sha>
git push
```

So với `kubectl rollout undo`: cách Git có tác giả, có thời gian, có lý do, và
apply lại được trên cluster khác.

### 3.7 Đọc log khi có sự cố

```bash
# Render chart hỏng -> ComparisonError
kubectl -n argocd logs deploy/argocd-repo-server --tail=50

# Sync và health
kubectl -n argocd logs statefulset/argocd-application-controller --tail=50

# Lý do cụ thể của một app
kubectl -n argocd get application kyc -o yaml | yq '.status.conditions'
```

### 3.8 Tạm tắt auto-sync để debug

```bash
kubectl -n argocd patch application kyc --type merge \
  -p '{"spec":{"syncPolicy":{"automated":null}}}'

# ... giờ bạn thử nghiệm thoải mái ...

kubectl -n argocd patch application kyc --type merge \
  -p '{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}'
```

### 3.9 AppProject chặn thật

```bash
# Thử thêm một ClusterRole vào chart service
cat >> deploy/charts/kyc/values.yaml <<'EOF'
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: ClusterRole
      metadata:
        name: kyc-khong-duoc-phep
      rules:
        - apiGroups: [""]
          resources: ["secrets"]
          verbs: ["get", "list"]
EOF
git commit -am "thu them clusterrole" && git push
```

Chờ đồng bộ rồi xem:

```bash
kubectl -n argocd get application kyc -o yaml | yq '.status.conditions'
```

Bạn sẽ thấy lỗi kiểu `ClusterRole is not permitted in project kalapa`. Ranh
giới đang làm việc.

```bash
git revert --no-edit HEAD && git push
```

---

## 4. Tự kiểm

- [ ] Bốn vấn đề của deploy kiểu đẩy?
- [ ] Bốn nguyên tắc của GitOps?
- [ ] Vì sao việc CI không giữ kubeconfig là tính chất bảo mật?
- [ ] Argo CD gồm những thành phần nào, mỗi cái làm gì?
- [ ] App of apps hoạt động thế nào? Bạn apply bằng tay mấy object?
- [ ] Sync wave giải quyết gì? Hơn `sleep 60` ở đâu?
- [ ] `prune: false` lấy mất của bạn điều gì?
- [ ] `selfHeal` sẽ làm gì với `kubectl scale` của bạn?
- [ ] `ignoreDifferences` sửa triệu chứng nào? Nhận ra bằng cách nào?
- [ ] Vì sao `:latest` làm chữ "Synced" vô nghĩa?
- [ ] AppProject với `clusterResourceWhitelist: []` ngăn được gì?
- [ ] Đọc log nào khi Application báo `ComparisonError`?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Vẫn `kubectl apply` bên cạnh Argo CD | `selfHeal` hoàn nguyên; bạn tưởng cluster hỏng |
| Dùng `:latest` | "Synced" nói dối bạn |
| Quên `finalizers` trên root app | Xoá root là bỏ rơi mọi con |
| Quên `include` khi `recurse: true` | Argo cố apply template Helm thô |
| Không có AppProject | Repo bị chiếm quyền = cluster bị chiếm quyền |
| Thiếu `ignoreDifferences` | `OutOfSync` vĩnh viễn, hoặc đánh nhau với controller |
| Bật TLS cho Argo CD sau ingress | Vòng lặp chuyển hướng |
| repo-server limit quá thấp | OOM lúc render chart lớn, lỗi rất khó hiểu |
| Commit secret chữ rõ | Git không quên (bài 14) |

---

**Bài tiếp:** [13 — CI/CD](13-cicd.md)
