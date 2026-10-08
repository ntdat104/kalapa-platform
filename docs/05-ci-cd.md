# 05 — CI/CD

Hai workflow. `.github/workflows/ci.yaml` build và phát hành các service Go;
`.github/workflows/manifests.yaml` kiểm tra phần Kubernetes.

---

## Ý tưởng cốt lõi

**CI không deploy.** Nó build một image, đẩy image đi, rồi ghi tag bất biến
mới vào `deploy/charts/<svc>/values.yaml` và commit. Argo CD thấy commit đó và
điều hoà cluster.

```
push ──► test ──► build image ──► đẩy lên GHCR
                                      │
                                      └─► ghi tag vào values.yaml,
                                          commit, push
                                                  │
                                      Argo CD ◄───┘  kéo về, đồng bộ
```

Cái đó mang lại:

| | |
|---|---|
| **Không có thông tin đăng nhập cluster trong CI** | workflow bị chiếm quyền cũng không với tới API server |
| **Việc deploy là một commit** | `git log deploy/charts/kyc/values.yaml` là lịch sử deploy, có tác giả và thời gian |
| **Rollback là `git revert`** | không phải một lệnh `kubectl rollout undo` phải nhớ kèm việc đoán số revision |
| **Dùng được cho mọi cluster** | cùng một commit deploy tới mọi cluster đang theo dõi đường dẫn đó |

---

## `ci.yaml`

### Job 1 — `changes`: tính matrix

```yaml
filters: |
  shared: &shared
    - 'go.mod'
    - 'internal/platform/**'
    - 'build/Dockerfile'
  kyc:
    - *shared
    - 'cmd/kyc/**'
    - 'internal/kyc/**'
```

Chỉ những service có thay đổi code mới được build lại — nhưng một thay đổi
trong `internal/platform/` sẽ build lại **tất cả**, vì code đó được biên dịch
vào cả ba binary. Viết sai cái neo YAML đó là cách bạn phát hành một service có
thư viện dùng chung đã đổi bên dưới, với một bản build màu xanh.

### Job 2 — `test`

```yaml
- run: go test -race -coverprofile=coverage.out ./...
```

`-race` mới là trọng tâm của bước này, không phải coverage. Ba thứ trong repo
này chia sẻ trạng thái giữa các goroutine — cờ readiness của HTTP server, vòng
lặp consumer Kafka, cache JWKS — và một data race ở đó sẽ hiện ra trong môi
trường thật dưới dạng dữ liệu hỏng, chứ không phải một test đỏ.

`govulncheck` cũng nằm ở đây thay vì một bộ quét dependency chung: nó phân tích
xem hàm nào có lỗ hổng *thực sự gọi tới được* từ code của bạn, nên không báo
động về một CVE nằm trên nhánh code bạn không bao giờ chạm.

### Job 3 — `build`

```yaml
push: ${{ github.event_name == 'push' }}
tags: |
  ghcr.io/${{ github.repository_owner }}/kalapa-${{ matrix.service }}:${{ steps.meta.outputs.short_sha }}
cache-from: type=gha,scope=${{ matrix.service }}
cache-to: type=gha,mode=max,scope=${{ matrix.service }}
```

PR vẫn build image (nên Dockerfile hỏng sẽ fail ở khâu review) nhưng không
đẩy. `mode=max` cache luôn cả các stage trung gian, nên một lớp dependency
không đổi sẽ không bao giờ build lại — thực tế là khác biệt giữa build 3 phút
và build 30 giây.

Trivy quét kết quả. Trên một image distroless tĩnh thì gần như không có bề mặt
gói hệ điều hành, nên một phát hiện ở đây là CVE thật của dependency Go chứ
không phải tiếng ồn về một base image mà bạn không vá được.

### Job 4 — `promote`: bàn giao

```yaml
- run: |
    yq -i ".\"go-service\".image.tag = \"${tag}\"" "deploy/charts/${svc}/values.yaml"
- run: |
    git commit -m "deploy: pin images to ${tag} [skip ci]"
    git pull --rebase origin main
    git push origin main
```

Ba chi tiết không phải tuỳ chọn:

- **`[skip ci]`** — thiếu nó, commit này kích hoạt lại pipeline, pipeline lại
  commit tiếp, lặp vô tận.
- **`git pull --rebase`** trước khi push — có thể có người đã push trong lúc
  build chạy.
- **`concurrency: cancel-in-progress`** ở đầu file — hai lần push cách nhau
  một phút sẽ tranh nhau ghi tag, và kẻ thua lại là kẻ thắng.

---

## Dockerfile

`build/Dockerfile`, ba stage, một file cho cả ba service.

```dockerfile
FROM golang:1.26-alpine AS deps
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
```

Dependency trước, nhờ vậy lớp cache module chỉ bị vô hiệu khi dependency đổi —
không phải mỗi lần sửa code.

```dockerfile
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}
```

| Cờ | Tác dụng |
|---|---|
| `CGO_ENABLED=0` | binary tĩnh, thứ làm cho stage cuối distroless khả thi |
| `-trimpath` | bỏ đường dẫn build; build tái lập được, và không lộ `/home/you/...` trong stack trace |
| `-ldflags="-s -w"` | bỏ bảng ký hiệu và thông tin DWARF — nhỏ hơn ~30% |

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
```

**Distroless là một biện pháp kiểm soát thật, không phải mỹ phẩm.** Không
shell, không trình quản lý gói, không binary setuid. Kẻ tấn công đã chạy được
code cũng không thể `curl | sh`, không thể `apt-get install`, thậm chí không
`ls` được. Nó xoá bỏ cả nhóm kỹ thuật "chui vào pod rồi đi tiếp".

Kết quả: **27 MB**, trong đó ~25 MB là binary Go.

`USER 65532` phải khớp `runAsUser: 65532` trong chart. Nếu lệch nhau, container
không đọc được chính file nhị phân của nó và crash-loop với `permission denied`.

---

## `manifests.yaml`

Dưới GitOps, một manifest sai không làm hỏng script deploy. Nó hỏng bên trong
Argo CD, nơi có thể một tiếng nữa bạn mới nhìn vào. Các kiểm tra này đưa lỗi
đó về pull request.

### Job `helm`

```bash
helm lint "$chart"
helm template "$name" "$chart" --namespace kalapa > "/tmp/${name}.yaml"
```

`lint` kiểm tra cấu trúc; `template` bắt được thứ lint không bắt được — một
template tham chiếu tới một value không ai định nghĩa sẽ hỏng ở đây chứ không
phải trong cluster.

Sau đó `kubeconform` kiểm tra mọi object đã render với OpenAPI schema thật của
Kubernetes, cộng thêm schema CRD từ kho cộng đồng. Cái này bắt được
`replicas: "1"` (chuỗi ở chỗ cần số nguyên) và tên trường viết sai, hai thứ mà
Helm render ra tỉnh bơ.

Rồi một lệnh grep tìm `__GIT_REPO_URL__`. Một placeholder bị push lên sẽ khiến
mọi Argo CD Application không phân giải được nguồn, với một lỗi chẳng nhắc gì
tới nguyên nhân.

### Job `policy`

Các khẳng định trên pod *đã render*:

```bash
check 'runAsNonRoot: true'
check 'allowPrivilegeEscalation: false'
check 'readOnlyRootFilesystem: true'
check 'automountServiceAccountToken: false'
```

Đây chính là các luật mà `pod-security.kubernetes.io/enforce: restricted` áp ở
admission. Kiểm tra chúng trong CI nghĩa là một bước lùi sẽ làm fail PR thay vì
fail lúc deploy — và thông báo lỗi nêu đích danh file.

Nó cũng cảnh báo khi gặp tag `:latest`, vì lý do nêu ở
[04-gitops-with-argocd.md](04-gitops-with-argocd.md#tag-image-và-vì-sao-latest-phá-vỡ-toàn-bộ-mô-hình).

Với cluster thật, hãy nâng cấp cái này lên một policy engine — **Kyverno** hoặc
**OPA Gatekeeper** — để cùng bộ luật đó được thực thi ở admission cho mọi thứ,
kể cả chart bạn không viết. Kiểm tra trong CI vẫn hữu ích như vòng phản hồi
nhanh.

### Job `secrets`

gitleaks, với `.gitleaks.toml` cho phép các thông tin đăng nhập dev có chủ ý.
Việc quét tồn tại để bắt cái thật mà ai đó dán vào sau này. YAS chạy nó hằng
đêm; chạy trên mọi PR nghĩa là rò rỉ không bao giờ lên tới `main`.

---

## Những gì nó chưa làm

Nói thẳng các khoảng trống:

| Thiếu | Vì sao | Nếu làm thì trông thế nào |
|---|---|---|
| Test tích hợp trên cluster thật | cần một cluster trong CI | `kind` + smoke test, dưới dạng một job |
| Progressive delivery | cần nhiều replica và một mesh hoặc ingress chia được traffic | Argo Rollouts: canary, phân tích, tự rollback |
| Ký image | ngoài phạm vi của một lab | `cosign sign` + policy Kyverno bắt buộc chữ ký hợp lệ |
| SBOM | như trên | `syft` trong job build, đính kèm như một attestation |
| Môi trường staging | chỉ có một cluster | một Argo CD Application thứ hai theo nhánh `staging`, hoặc một thư mục `environments/` |

Cái staging là thứ thú vị nhất nên thêm tiếp. Hình dạng thông thường là một
thư mục cho mỗi môi trường với lớp values riêng, và việc thăng hạng là một PR
chép tag image từ `environments/staging` sang `environments/production`.

---

## Danh sách tự kiểm

- [ ] Vì sao CI commit thay vì gọi `kubectl`
- [ ] Một thay đổi trong `internal/platform/` phải build lại những gì, và vì sao
- [ ] Vì sao `-race` quan trọng hơn coverage ở đây
- [ ] `[skip ci]` ngăn chuyện gì
- [ ] `CGO_ENABLED=0` mở ra khả năng gì ở stage sau
- [ ] Distroless thực sự loại bỏ cái gì
- [ ] Vì sao `USER` trong Dockerfile và `runAsUser` trong chart phải khớp
- [ ] Vì sao `helm template` bắt được thứ `helm lint` không bắt được
