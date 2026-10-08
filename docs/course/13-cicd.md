# 13 — CI/CD

> **Bài này trả lời:** Từ `git push` đến pod mới chạy, có những gì ở giữa? Và
> vì sao pipeline ở đây lại *không* gọi `kubectl`?
>
> **Cần xong bài:** [12](12-gitops-argocd.md)

---

## 1. Lý thuyết

### 1.1 CI và CD

| Từ | Nghĩa |
|---|---|
| **CI** (Continuous Integration) | Mỗi commit được build và kiểm thử tự động |
| **CD** (Continuous Delivery) | Mỗi commit đạt đều **sẵn sàng** phát hành |
| **CD** (Continuous Deployment) | Mỗi commit đạt được **tự động phát hành** |

Repo này làm CI + Continuous Deployment, nhưng theo kiểu **kéo** (bài 12), nên
ranh giới nằm ở chỗ khác chỗ thường thấy.

### 1.2 Một pipeline tốt gồm gì

Theo thứ tự tăng dần về chi phí — thứ rẻ chạy trước để fail sớm:

```
1. Định dạng       gofmt          (giây)
2. Phân tích tĩnh  go vet, lint   (giây)
3. Unit test       go test -race  (chục giây)
4. Build           docker build   (phút)
5. Quét bảo mật    trivy          (chục giây)
6. Phát hành       push + commit  (giây)
```

### 1.3 Bàn giao sang GitOps

Pipeline truyền thống kết thúc bằng `kubectl apply`. Ở đây nó kết thúc bằng
**một commit**:

```
push ──► test ──► build image ──► đẩy lên registry
                                      │
                                      └─► ghi tag mới vào values.yaml,
                                          commit, push
                                                  │
                                      Argo CD ◄───┘  kéo về, đồng bộ
```

Lợi ích đã nói ở bài 12. Điều cần nhấn mạnh: **tag phải bất biến**. Ghi
`:latest` vào values thì chẳng thay đổi gì cả.

### 1.4 Ba chi tiết dễ sai ở bước commit

**`[skip ci]`** — thiếu nó, commit này kích hoạt lại pipeline, pipeline lại
commit, lặp vô tận.

**`git pull --rebase`** trước khi push — có thể có người đã push trong lúc
build chạy.

**`concurrency: cancel-in-progress`** — hai lần push cách nhau một phút sẽ
tranh nhau ghi tag, và **kẻ thua lại là kẻ thắng** (commit của build cũ ghi
đè build mới).

### 1.5 Build có chọn lọc

Repo một kho (monorepo) với ba service. Sửa `cmd/kyc` thì build lại cả ba là
lãng phí.

Nhưng cẩn thận: sửa `internal/platform/` thì **phải build lại cả ba**, vì code
đó được biên dịch vào cả ba binary. Viết sai bộ lọc là cách bạn phát hành một
service có thư viện dùng chung đã đổi bên dưới — với một bản build màu xanh.

### 1.6 Vì sao `-race` quan trọng hơn coverage

Coverage đo "bao nhiêu dòng được chạy". Nó không nói gì về đúng sai.

`-race` phát hiện **data race** — hai goroutine truy cập cùng biến mà không
đồng bộ. Lỗi này:

- không xuất hiện mỗi lần chạy
- biểu hiện là **dữ liệu hỏng**, không phải crash
- gần như không thể tái hiện khi debug

Trong repo này có ba chỗ chia sẻ trạng thái giữa goroutine: cờ readiness của
HTTP server, vòng lặp consumer Kafka, và cache JWKS.

### 1.7 Quét bảo mật

| Công cụ | Quét gì |
|---|---|
| `govulncheck` | Lỗ hổng trong dependency Go **thực sự gọi tới được** từ code bạn |
| `trivy` | CVE trong image (gói hệ điều hành + thư viện ứng dụng) |
| `gitleaks` | Thông tin đăng nhập bị commit nhầm |

`govulncheck` khác bộ quét manifest thông thường: nó phân tích đồ thị lời gọi,
nên không báo động về CVE nằm trên nhánh code bạn không bao giờ chạm.

### 1.8 Kiểm tra manifest

Dưới GitOps, một manifest sai **không làm hỏng script deploy**. Nó hỏng bên
trong Argo CD, nơi có thể một tiếng nữa bạn mới nhìn vào.

Nên pipeline phải kiểm tra:

| Kiểm tra | Bắt được gì |
|---|---|
| `helm lint` | Lỗi cấu trúc chart |
| `helm template` | Value thiếu, template hỏng — thứ `lint` không bắt |
| `kubeconform` | Sai schema: `replicas: "1"` (chuỗi), tên trường viết sai |
| Khẳng định policy | `runAsNonRoot` bị gỡ mất, thiếu `resources` |
| `gitleaks` | Secret bị commit |

---

## 2. Trong repo này nằm đâu

Hai workflow: `.github/workflows/ci.yaml` và
`.github/workflows/manifests.yaml`.

### Job `changes` — tính matrix

`.github/workflows/ci.yaml:36`

```yaml
      - uses: dorny/paths-filter@v3
        id: filter
        with:
          filters: |
            shared: &shared
              - 'go.mod'
              - 'go.sum'
              - 'internal/platform/**'
              - 'build/Dockerfile'
              - '.github/workflows/ci.yaml'
            kyc:
              - *shared
              - 'cmd/kyc/**'
              - 'internal/kyc/**'
```

`&shared` và `*shared` là **neo YAML**: định nghĩa một lần, dùng lại. Mỗi
service gồm phần dùng chung **cộng** phần riêng của nó.

> Đây chính là mục 1.5 hiện ra thành code. Bỏ `*shared` khỏi một service là
> service đó sẽ không được build lại khi thư viện dùng chung thay đổi.

### Job `test`

`.github/workflows/ci.yaml:68`

```yaml
      - name: gofmt
        run: |
          unformatted=$(gofmt -l ./cmd ./internal)
          if [ -n "$unformatted" ]; then
            echo "::error::not gofmt'd: $unformatted"
            exit 1
          fi
```

> `gofmt` luôn trả về 0 dù có tìm thấy gì hay không. Kiểm tra phải dựa vào
> việc nó **có in ra tên file hay không**. Viết `gofmt -l ... && exit 1` là sai.

```yaml
      - name: Tests with race detector
        run: go test -race -coverprofile=coverage.out -covermode=atomic ./...
```

```yaml
      - name: govulncheck
        run: |
          go install golang.org/x/vuln/cmd/govulncheck@latest
          govulncheck ./...
```

### Job `build`

`.github/workflows/ci.yaml:111`

```yaml
    strategy:
      fail-fast: false
      matrix:
        service: ${{ fromJSON(needs.changes.outputs.services) }}
```

Matrix được tính động từ job `changes`.

```yaml
      - id: meta
        run: echo "short_sha=$(git rev-parse --short=12 HEAD)" >> "$GITHUB_OUTPUT"
```

Đây là **tag bất biến**.

```yaml
        with:
          push: ${{ github.event_name == 'push' }}
          cache-from: type=gha,scope=${{ matrix.service }}
          cache-to: type=gha,mode=max,scope=${{ matrix.service }}
```

> PR **vẫn build** image (nên Dockerfile hỏng sẽ fail ở khâu review) nhưng
> **không đẩy**. `mode=max` cache cả stage trung gian — chính là cache layer
> của bài 02, nhưng là trên runner của GitHub.

### Job `promote` — bàn giao

`.github/workflows/ci.yaml:182`

```yaml
      - name: Pin the new image tags
        run: |
          for svc in $(echo '${{ needs.changes.outputs.services }}' | jq -r '.[]'); do
            f="deploy/charts/${svc}/values.yaml"
            yq -i ".\"go-service\".image.tag = \"${tag}\"" "$f"
            git add "$f"
          done
```

```yaml
      - name: Commit and push
        run: |
          git commit -m "deploy: pin images to ${{ needs.build.outputs.tag }} [skip ci]"
          git pull --rebase origin main
          git push origin main
```

Và ở đầu file, dòng 24:

```yaml
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
```

Ba chi tiết ở mục 1.4, cả ba đều có mặt.

> **Để ý permissions.** Job `test` và `build` chỉ có `contents: read`; job
> `promote` mới có `contents: write`. Và **không job nào** có thông tin đăng
> nhập cluster.

### Workflow kiểm tra manifest

`.github/workflows/manifests.yaml`, job `helm`:

```bash
            helm lint "$chart"
            helm template "$name" "$chart" --namespace kalapa > "/tmp/${name}.yaml"
```

Rồi `kubeconform` với schema CRD từ kho cộng đồng:

```bash
          /tmp/kubeconform -strict -summary \
            -schema-location default \
            -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' \
            -ignore-missing-schemas /tmp/*.yaml
```

Và một kiểm tra rất thực tế:

```yaml
      # trích từ .github/workflows/manifests.yaml
      - name: Check for the unsubstituted repo placeholder
        run: |
          if grep -rq '__GIT_REPO_URL__' deploy/argocd deploy/manifests; then
            echo "::error::run scripts/init-repo.sh before committing"
            exit 1
          fi
```

> Một placeholder bị push lên sẽ khiến **mọi** Argo CD Application không phân
> giải được nguồn, với lỗi chẳng nhắc gì tới nguyên nhân.

Job `policy` — khẳng định trên pod đã render:

```bash
            check 'runAsNonRoot: true'            'runAsNonRoot'
            check 'allowPrivilegeEscalation: false' 'allowPrivilegeEscalation: false'
            check 'readOnlyRootFilesystem: true'  'readOnlyRootFilesystem'
            check 'automountServiceAccountToken: false' 'automountServiceAccountToken: false'
```

> Đây chính là bộ luật mà Pod Security Admission áp ở admission (bài 14).
> Kiểm tra trong CI nghĩa là một bước lùi làm **fail PR** thay vì fail lúc
> deploy — và thông báo lỗi nêu đích danh file.

Và cảnh báo `:latest`:

```bash
# trích từ job `policy`, chạy trên từng file đã render
if grep -qE 'image: .*:latest"?$' "$f"; then
  echo "::warning file=$f::$name still uses a :latest tag"
fi
```

### Dockerfile

Đã học ở bài 02. Nhắc lại mối nối: `CGO_ENABLED=0` → binary tĩnh → stage cuối
distroless → `USER 65532` phải khớp `runAsUser` trong chart.

---

## 3. Thực hành

### 3.1 Chạy đúng các kiểm tra đó trên máy

```bash
cd ~/Documents/resource/kalapa-platform

go vet ./...
test -z "$(gofmt -l ./cmd ./internal)" && echo "gofmt sạch"
go test -race ./...
make lint
make render
```

> Chạy được trên máy nghĩa là CI sẽ xanh. Vòng lặp phản hồi nhanh hơn nhiều so
> với push rồi chờ.

### 3.2 Tự tay tạo một data race

```bash
cat > /tmp/race_test.go <<'EOF'
package obs

import (
	"sync"
	"testing"
)

func TestCoRace(t *testing.T) {
	dem := 0
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); dem++ }()   // KHÔNG đồng bộ
	}
	wg.Wait()
	t.Log(dem)
}
EOF
cp /tmp/race_test.go internal/platform/obs/
go test ./internal/platform/obs/              # có thể PASS
go test -race ./internal/platform/obs/        # FAIL với WARNING: DATA RACE
rm internal/platform/obs/race_test.go
```

Chú ý: lần chạy không có `-race` **có thể pass**. Đó chính là lý do `-race`
tồn tại.

### 3.3 Chạy thử kiểm tra policy

```bash
make render
for f in /tmp/kalapa-rendered/{gateway,kyc,scoring}.yaml; do
  n=$(basename "$f" .yaml)
  for chk in 'runAsNonRoot: true' 'allowPrivilegeEscalation: false' \
             'readOnlyRootFilesystem: true' 'automountServiceAccountToken: false'; do
    grep -q "$chk" "$f" || echo "  ✗ $n thiếu: $chk"
  done
  echo "  ✓ $n"
done
```

Giờ phá nó:

```bash
# Tạm tắt readOnlyRootFilesystem
sed -i.bak 's/readOnlyRootFilesystem: true/readOnlyRootFilesystem: false/' \
  deploy/charts/go-service/values.yaml
make render
grep -c 'readOnlyRootFilesystem: true' /tmp/kalapa-rendered/kyc.yaml    # -> 0
mv deploy/charts/go-service/values.yaml.bak deploy/charts/go-service/values.yaml
```

Trên CI, thay đổi đó sẽ làm fail PR.

### 3.4 Kubeconform

```bash
# Cài (macOS)
brew install kubeconform 2>/dev/null || \
  { curl -sSLo /tmp/kc.tar.gz https://github.com/yannh/kubeconform/releases/latest/download/kubeconform-darwin-arm64.tar.gz && tar -xzf /tmp/kc.tar.gz -C /tmp; }

make render
kubeconform -strict -summary -ignore-missing-schemas /tmp/kalapa-rendered/*.yaml 2>/dev/null \
  || /tmp/kubeconform -strict -summary -ignore-missing-schemas /tmp/kalapa-rendered/*.yaml
```

Thử tạo lỗi schema:

```bash
echo '
  replicaCount: "2"' >> deploy/charts/kyc/values.yaml
make render 2>&1 | tail -3
# kubeconform sẽ bắt được kiểu dữ liệu sai
git checkout deploy/charts/kyc/values.yaml
```

### 3.5 Theo dõi pipeline thật

Sau khi `make init` và push:

```bash
gh run list --limit 5
gh run watch
gh run view --log | tail -40
```

Và xem commit mà pipeline tự tạo:

```bash
git pull
git log --oneline -3
git show --stat HEAD
```

Bạn sẽ thấy commit `deploy: pin images to <sha> [skip ci]` chỉ sửa đúng
`deploy/charts/*/values.yaml`.

### 3.6 Nhìn vòng lặp khép kín

```bash
# 1. Sửa code
sed -i.bak 's/300 + int(h.Sum32()%551)/310 + int(h.Sum32()%541)/' internal/scoring/scoring.go
rm -f internal/scoring/scoring.go.bak
go test ./internal/scoring/

# 2. Push
git commit -am "scoring: doi khoang diem" && git push

# 3. Xem CI
gh run watch

# 4. Xem commit CI tạo ra
git pull && git log --oneline -2

# 5. Xem Argo CD đồng bộ
kubectl -n argocd get application scoring -w

# 6. Xem tag image của pod
kubectl -n kalapa get deploy scoring \
  -o jsonpath='{.spec.template.spec.containers[0].image}'; echo
```

Tag ở bước 6 phải là short SHA, **không phải `latest`**. Đó là toàn bộ chuỗi
từ bài 02 đến bài 13 khép lại.

---

## 4. Tự kiểm

- [ ] CI, Continuous Delivery, Continuous Deployment khác nhau thế nào?
- [ ] Vì sao kiểm tra rẻ phải chạy trước?
- [ ] Vì sao pipeline này commit thay vì gọi `kubectl`?
- [ ] `[skip ci]` ngăn chuyện gì?
- [ ] Vì sao cần `git pull --rebase` trước khi push?
- [ ] `concurrency: cancel-in-progress` ngăn chuyện gì?
- [ ] Sửa `internal/platform/` phải build lại những service nào? Vì sao?
- [ ] Vì sao `-race` quan trọng hơn coverage?
- [ ] `govulncheck` khác bộ quét dependency thường ở đâu?
- [ ] `helm lint` và `helm template` bắt được những gì khác nhau?
- [ ] Kiểm tra policy trong CI trùng với cơ chế nào của Kubernetes?
- [ ] Job nào trong pipeline có quyền ghi? Job nào có kubeconfig?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Quên `[skip ci]` | Pipeline tự kích hoạt vô tận |
| Bộ lọc đường dẫn thiếu `shared` | Service không được build lại khi thư viện chung đổi |
| Dùng `gofmt -l ... && exit 1` | `gofmt` luôn trả 0; phải kiểm tra output |
| Bỏ `-race` cho nhanh | Data race lọt ra production dưới dạng dữ liệu hỏng |
| Ghi `:latest` vào values | Chẳng thay đổi gì, Argo CD vẫn mù |
| Cho CI quyền cluster | Workflow bị chiếm quyền = cluster bị chiếm quyền |
| Chỉ `helm lint` | Value thiếu lọt qua, hỏng lúc apply |
| Không có kiểm tra policy | `runAsNonRoot` bị gỡ mất mà không ai thấy |
| Không quét secret | Một `git push` nhầm là phải xoay toàn bộ khoá |

---

**Bài tiếp:** [14 — Bảo mật](14-bao-mat.md)
