# 14 — Bảo mật

> **Bài này trả lời:** Những thứ tối thiểu phải làm là gì, và mỗi thứ chặn
> được kiểu tấn công nào?
>
> **Cần xong bài:** [13](13-cicd.md)

---

## 1. Lý thuyết

### 1.1 Phòng thủ nhiều lớp

Không có lớp nào là đủ. Nguyên tắc: giả định mỗi lớp **sẽ** thủng, và xếp
chúng sao cho thủng một lớp không đồng nghĩa với mất tất cả.

```
┌─ Image             không shell, không trình quản lý gói
├─ Container         non-root, không leo thang quyền, filesystem chỉ-đọc
├─ Pod Security      admission từ chối pod vi phạm
├─ RBAC              service account không có quyền gì
├─ Mạng              NetworkPolicy chặn luồng không cần thiết
├─ Secret            không nằm trong ConfigMap, không nằm trong Git chữ rõ
└─ Chuỗi cung ứng    quét dependency, quét image, quét secret
```

### 1.2 Lớp image

Image càng ít thứ càng tốt — không phải vì dung lượng, mà vì **kẻ tấn công
dùng được ít công cụ hơn**.

Kẻ tấn công vừa chạy được code trong container của bạn muốn gì? `curl` để tải
payload, `sh` để chạy nó, `apt` để cài thêm, `ps`/`netstat` để trinh sát.

Image **distroless** không có cái nào. Nó chứa đúng: chứng chỉ CA, thông tin
múi giờ, vài file user. Không shell nghĩa là `exec` vào container cũng không
làm gì được — và đó là điều tốt.

### 1.3 Lớp container — securityContext

| Thiết lập | Chặn gì |
|---|---|
| `runAsNonRoot: true` | Root trong container ánh xạ sang UID 0 của host; thoát container là thành root máy chủ |
| `allowPrivilegeEscalation: false` | Chặn binary setuid giành thêm quyền |
| `capabilities: drop: ["ALL"]` | Bỏ cả bộ capability mặc định. Binary Go tĩnh không cần cái nào |
| `readOnlyRootFilesystem: true` | Không ghi được payload vào đĩa. Buộc kẻ tấn công chỉ ở trong bộ nhớ |
| `seccompProfile: RuntimeDefault` | Chặn ~44 syscall hiếm dùng, nơi nhiều lỗ hổng escape nằm |

> `readOnlyRootFilesystem` đòi phải có chỗ ghi tạm — nên chart mount một
> `emptyDir` vào `/tmp` khi bật nó.

### 1.4 Pod Security Admission

securityContext là thứ bạn **tự nguyện** khai báo. PSA là thứ **bắt buộc** ở
mức namespace.

Ba profile:

| Profile | Cho phép |
|---|---|
| `privileged` | mọi thứ |
| `baseline` | chặn hiển nhiên: host namespace, container privileged, hostPath |
| `restricted` | thêm: non-root, không leo thang quyền, bỏ hết capability, bắt buộc seccomp |

Bật bằng label của namespace. Và có một hành vi **cực kỳ dễ gây nhầm**:

> Deployment chỉ nhận một **cảnh báo**. Các **Pod** mới bị **từ chối**.
>
> Nghĩa là: `kubectl apply` thành công, Deployment tồn tại, và **không pod nào
> xuất hiện**. Lý do chỉ nằm trong event của ReplicaSet.

```bash
kubectl -n kalapa describe rs -l app.kubernetes.io/name=<svc> | grep -A3 FailedCreate
```

Hãy nhớ lệnh này.

### 1.5 RBAC và service account

Mỗi pod chạy dưới một ServiceAccount. Mặc định, Kubernetes **mount token của
nó vào pod** tại `/var/run/secrets/kubernetes.io/serviceaccount/token`.

Nếu ứng dụng không gọi API server — mà hầu hết không gọi — thì token đó chỉ là
**bề mặt tấn công**: một thông tin đăng nhập nằm sẵn trong filesystem chờ bị
lấy.

```yaml
automountServiceAccountToken: false
```

### 1.6 Lớp mạng — NetworkPolicy

Mặc định trong Kubernetes: **mọi pod gọi được mọi pod**, kể cả khác namespace.
Một pod bị chiếm quyền có thể quét toàn bộ cluster.

NetworkPolicy giới hạn luồng. Và có một quy tắc giải thích mọi sự bối rối:

> Một pod là **mặc định cho phép** cho tới khi có policy chọn nó. Ngay khi có,
> pod đó thành **mặc định từ chối** cho các hướng mà policy liệt kê.

Nghĩa là **policy đầu tiên bạn viết sẽ khoá pod lại**, và bạn phải mở lại mọi
luồng nó cần — **bắt đầu bằng DNS**. Quên DNS là mọi lời gọi ra ngoài chết ở
bước phân giải tên, trong khi log đổ lỗi cho service đích.

> **Và một điều quan trọng:** CNI mặc định của minikube **bỏ qua** 
> NetworkPolicy. Object được chấp nhận rồi không làm gì. Một policy bạn chưa
> kiểm chứng là một policy bạn không có.

### 1.7 Secret

Đã học ở bài 06: Secret chỉ **base64**, không mã hoá.

Với GitOps, câu hỏi thành: làm sao để trạng thái mong muốn nằm trong Git mà
mật khẩu không nằm trong Git dưới dạng chữ rõ?

| Cách | Hoạt động | Đánh đổi |
|---|---|---|
| **Sealed Secrets** | Mã hoá bằng khoá công khai của cluster; commit bản mã | Đơn giản; khoá gắn với một cluster, khôi phục thảm hoạ cần sao lưu khoá |
| **External Secrets** | Commit một *tham chiếu*; operator kéo từ Vault/AWS SM | Không có bản mã trong Git; thêm operator và phụ thuộc ngoài |
| **SOPS + age** | Mã hoá ngay trong repo; giải mã lúc apply | Trải nghiệm tốt; Argo CD cần plugin |

Dù chọn cách nào, quy tắc không đổi: **không bao giờ `kubectl create secret`
bằng tay.** Secret chỉ tồn tại trong cluster là trạng thái không tái tạo được.

### 1.8 Bảo mật chuỗi cung ứng

Code của bạn là phần nhỏ của thứ đang chạy. Phần lớn là dependency.

| Kiểm tra | Bắt gì |
|---|---|
| `govulncheck` | CVE trong dependency Go **thực sự gọi tới được** |
| `trivy` | CVE trong image |
| `gitleaks` | Thông tin đăng nhập bị commit |
| Ghim phiên bản | Chặn tấn công "phiên bản độc hại được đẩy lên registry" |

### 1.9 Xác thực JWT — ba thứ thực sự hay hỏng

Khi gateway verify token của Keycloak:

**1. Không khớp issuer.** Keycloak đóng dấu `iss` bằng URL mà **trình duyệt**
dùng. Một pod verify theo tên Service nội bộ sẽ thấy lệch. Cách sửa là cấu
hình hostname cho đúng, **không phải** nới lỏng việc kiểm tra.

**2. Xoay khoá.** Keycloak tự xoay khoá ký. Bên verify phải nạp lại JWKS khi
gặp một `kid` lạ.

**3. Lệch đồng hồ.** So sánh `exp` quá nghiêm làm token hỏng ngắt quãng theo
cách trông như lỗi mạng. Cần một khoảng dung sai.

Và một thứ **không bao giờ được bỏ qua**: **ghim thuật toán**.

```go
if header.Alg != "RS256" {
    return nil, fmt.Errorf("unexpected alg %q", header.Alg)
}
```

Thiếu dòng này, kẻ tấn công đặt `alg: none` hoặc đổi sang HS256 rồi ký bằng
chính khoá công khai (vốn công khai). Hai lỗ hổng kinh điển, chặn bằng một
dòng.

---

## 2. Trong repo này nằm đâu

### Image

`build/Dockerfile:34`

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
```

### securityContext

`deploy/charts/go-service/values.yaml:89`

```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532                 # phải khớp USER trong build/Dockerfile
  runAsGroup: 65532
  fsGroup: 65532
  seccompProfile:
    type: RuntimeDefault

securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  runAsUser: 65532
```

Và `emptyDir` cho `/tmp` trong `deploy/charts/go-service/templates/deployment.yaml`:

```yaml
        {{- if .Values.securityContext.readOnlyRootFilesystem }}
        - name: tmp
          emptyDir:
            medium: Memory
            sizeLimit: 16Mi
        {{- end }}
```

> `medium: Memory` nghĩa là tmpfs — không chạm đĩa của node.

### Pod Security Admission

`deploy/manifests/namespaces.yaml:20`

```yaml
  name: kalapa
  labels:
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
    pod-security.kubernetes.io/warn: restricted
```

Và các ngoại lệ, với lý do ghi ngay trong file:

| Namespace | Mức | Vì sao |
|---|---|---|
| `kalapa` | `restricted` | code của ta, ta kiểm soát |
| `data`, `kafka`, `identity` | `baseline` | operator cần init container đặc quyền |
| `observability` | `privileged` | node-exporter mount filesystem và dùng network của host |

> Việc bị **buộc phải ghi ngoại lệ ra một file** mới là phần hữu ích. Nó biến
> "chúng tôi chạy node-exporter đặc quyền" từ một thứ không ai biết thành một
> dòng trong Git.

**Và đây là bằng chứng nó thực sự có hiệu lực:** khi dựng repo này, Reloader —
một chart của bên thứ ba — đã bị PSA từ chối. Cách sửa nằm ở
`deploy/argocd/platform/03-reloader.yaml`:

```yaml
            securityContext:
              runAsNonRoot: true
              runAsUser: 65534
              seccompProfile:
                type: RuntimeDefault
            containerSecurityContext:
              allowPrivilegeEscalation: false
              readOnlyRootFilesystem: true
              capabilities:
                drop: ["ALL"]
```

Chú thích ngay trên đó ghi lại chính xác triệu chứng: Deployment được tạo, mọi
Pod bị từ chối, và manh mối duy nhất là một event `FailedCreate`.

### Service account

`deploy/charts/go-service/values.yaml:117`

```yaml
automountServiceAccountToken: false
```

Mỗi service vẫn có ServiceAccount riêng (`serviceAccount.create: true`) —
không tốn gì, và nghĩa là sau này thêm một luật RBAC sẽ không nới quyền cho cả
ba service cùng lúc.

### NetworkPolicy

`deploy/charts/go-service/templates/networkpolicy.yaml`. Để ý luật DNS:

```yaml
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
```

Và luật cho Prometheus:

```yaml
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: observability
      ports:
        - port: admin
```

> Thiếu luật này thì **mọi target của Prometheus down cùng lúc** ngay khi bạn
> bật NetworkPolicy.

Mặc định tắt (`deploy/charts/go-service/values.yaml:189`), với lý do ghi trong template: CNI của
minikube không thực thi nó.

Và cấu hình theo service, ví dụ `deploy/charts/kyc/values.yaml`:

```yaml
  networkPolicy:
    enabled: false
    allowFromPods:
      - gateway            # CHỈ gateway được gọi kyc
    allowToNamespaces:
      - data
      - kafka
      - observability
```

### Xác thực JWT

`internal/gateway/auth.go`. Bốn chỗ tương ứng mục 1.9:

**Ghim thuật toán:**

```go
	// Ghim thuật toán đóng cả hai họ tấn công `alg: none` và nhầm lẫn khoá
	// HS256 chỉ bằng một dòng.
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unexpected alg %q", header.Alg)
	}
```

**Kiểm tra issuer:**

```go
	if a.IssuerURL != "" && strings.TrimSuffix(claims.Issuer, "/") != a.IssuerURL {
		// Đây là lỗi bạn sẽ gặp đầu tiên. Keycloak đóng dấu `iss` bằng URL mà
		// TRÌNH DUYỆT dùng... Sửa bằng cách cấu hình hostname của Keycloak,
		// không phải bằng cách nới lỏng kiểm tra này.
		return nil, fmt.Errorf("issuer mismatch: ...")
	}
```

**Dung sai đồng hồ:**

```go
	// 60s dung sai: pod và đồng hồ host lệch nhau, và so sánh nghiêm ngặt làm
	// token hỏng ngắt quãng theo cách trông như lỗi mạng.
	if claims.Expiry > 0 && now.After(time.Unix(claims.Expiry, 0).Add(60*time.Second)) {
```

**Xoay khoá**, trong hàm `keyFor`:

```go
	// trả về khoá ký, làm mới JWKS tối đa mỗi chu kỳ cache — và NGAY LẬP TỨC
	// khi gặp kid lạ, đó là cách xoay khoá phải được xử lý.
```

Và phần test: `internal/gateway/auth_test.go` kiểm chứng cả bảy trường hợp —
token hợp lệ, sai khoá, sai thuật toán, sai issuer, hết hạn, sai audience, và
token dị dạng.

### Secret

`deploy/charts/kalapa-config/templates/secret.yaml` — khối chú thích ở đầu file
nói thẳng vì sao mật khẩu chữ rõ chấp nhận được **ở đây và không ở đâu khác**,
rồi liệt kê ba lựa chọn thật.

`.gitleaks.toml` cho phép các giá trị giả có chủ ý:

```toml
[allowlist]
description = "Known-fake development credentials"
regexes = [
  '''kalapa-dev-password''',
  ...
]
```

> Danh sách cho phép hẹp, theo từng giá trị cụ thể. Nhờ vậy một mật khẩu
> **thật** dán nhầm vào vẫn bị bắt.

### Chuỗi cung ứng

`.github/workflows/ci.yaml` — `govulncheck` và `trivy`.
`.github/workflows/manifests.yaml` — `gitleaks` và job `policy`.

---

## 3. Thực hành

### 3.1 Chứng minh distroless không cho bạn làm gì

```bash
POD=$(kubectl -n kalapa get pod -l app.kubernetes.io/name=kyc -o name | head -1)
kubectl -n kalapa exec $POD -- sh          # lỗi
kubectl -n kalapa exec $POD -- ls /        # lỗi
kubectl -n kalapa exec $POD -- cat /etc/passwd  # lỗi
```

Cần debug thì dùng ephemeral container (bài 06):

```bash
kubectl -n kalapa debug $POD -it --image=busybox:1.36 --target=kyc -- sh
```

### 3.2 Xác nhận securityContext đang có hiệu lực

```bash
kubectl -n kalapa get pod $POD -o jsonpath='{.spec.containers[0].securityContext}' | jq
kubectl -n kalapa debug $POD -it --image=busybox:1.36 --target=kyc -- \
  sh -c 'cat /proc/1/status | grep -E "^(Uid|Gid|CapEff|NoNewPrivs|Seccomp)"'
```

| Dòng | Kỳ vọng |
|---|---|
| `Uid` | `65532` — không phải 0 |
| `CapEff` | `0000000000000000` — không capability nào |
| `NoNewPrivs` | `1` — không leo thang quyền được |
| `Seccomp` | `2` — filter đang bật |

### 3.3 Chứng minh PSA thực sự chặn — bài quan trọng nhất

```bash
kubectl -n kalapa run bad --image=nginx --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"bad","image":"nginx","securityContext":{"privileged":true}}]}}'
```

Bị từ chối ngay. Giờ xem hành vi dễ nhầm — Deployment được chấp nhận nhưng Pod
thì không:

```bash
kubectl -n kalapa create deployment bad-deploy --image=nginx
kubectl -n kalapa get deploy bad-deploy        # TỒN TẠI
kubectl -n kalapa get pods -l app=bad-deploy   # KHÔNG CÓ POD NÀO
kubectl -n kalapa describe rs -l app=bad-deploy | grep -A3 FailedCreate
```

**Đây là thông báo bạn phải thuộc mặt.** Dọn dẹp:

```bash
kubectl -n kalapa delete deployment bad-deploy
```

So sánh với namespace lỏng hơn:

```bash
kubectl -n data run thu --image=nginx --restart=Never    # chạy được (baseline)
kubectl -n data delete pod thu
```

### 3.4 Token service account

```bash
kubectl -n kalapa debug $POD -it --image=busybox:1.36 --target=kyc -- \
  ls -la /proc/1/root/var/run/secrets/kubernetes.io/ 2>&1
```

Không có gì — `automountServiceAccountToken: false` đang làm việc.

So sánh với một pod mặc định:

```bash
kubectl -n data run co-token --image=busybox:1.36 --restart=Never -- sleep 300
sleep 5
kubectl -n data exec co-token -- ls /var/run/secrets/kubernetes.io/serviceaccount/
kubectl -n data exec co-token -- head -c 60 /var/run/secrets/kubernetes.io/serviceaccount/token; echo
kubectl -n data delete pod co-token
```

Đó là một thông tin đăng nhập nằm sẵn trong filesystem.

### 3.5 Chứng minh mạng đang mở toang

```bash
kubectl -n kalapa run quet --rm -it --image=busybox:1.36 --restart=Never -- sh
```

Trong shell:

```sh
wget -qO- --timeout=3 http://kyc.kalapa/kyc/applications        # được
nc -z -w2 kalapa-db-rw.data 5432 && echo "tới được database"     # được!
nc -z -w2 kalapa-kafka-bootstrap.kafka 9092 && echo "tới được kafka"
exit
```

Một pod bất kỳ gọi thẳng được vào database. **Đó là mặc định.**

Bật NetworkPolicy:

```bash
# Trong deploy/charts/kyc/values.yaml -> networkPolicy.enabled: true
# rồi helm upgrade hoặc commit & push
```

Nhưng nhớ: trên CNI mặc định của minikube, policy **không có hiệu lực**. Muốn
kiểm chứng thật:

```bash
minikube delete -p kalapa
CNI=calico MEMORY=8g make cluster && make up-local
```

### 3.6 Secret chỉ là base64 (nhắc lại cho thấm)

```bash
kubectl -n kalapa get secret kalapa-postgres-credentials \
  -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d; echo
```

### 3.7 Chạy quét bảo mật trên máy

```bash
cd ~/Documents/resource/kalapa-platform

go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...

docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:quet .
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  aquasec/trivy:latest image --severity HIGH,CRITICAL --ignore-unfixed kalapa-kyc:quet

docker run --rm -v "$PWD:/work" -w /work zricethezav/gitleaks:v8.21.2 \
  detect --source=. --config=/work/.gitleaks.toml --redact --verbose
```

### 3.8 Thử tấn công JWT

```bash
cd ~/Documents/resource/kalapa-platform
go test ./internal/gateway/ -run TestVerify -v 2>&1 | grep -E "^(=== RUN|--- )"
```

Bạn sẽ thấy các trường hợp: `rejects_an_unexpected_algorithm`,
`rejects_a_foreign_issuer`, `rejects_a_token_signed_by_another_key`…

Giờ **tự tay gỡ lớp bảo vệ** để thấy nó quan trọng:

```bash
sed -i.bak 's/if header.Alg != "RS256" {/if false {/' internal/gateway/auth.go
go test ./internal/gateway/ -run TestVerify 2>&1 | tail -5
mv internal/gateway/auth.go.bak internal/gateway/auth.go
go test ./internal/gateway/ -run TestVerify
```

Test `rejects_an_unexpected_algorithm` sẽ fail. Đó là một dòng code đứng giữa
bạn và một lỗ hổng xác thực.

---

## 4. Tự kiểm

- [ ] Bảy lớp phòng thủ trong bài này, mỗi lớp chặn gì?
- [ ] Distroless loại bỏ cái gì? Vì sao điều đó quan trọng khi bị tấn công?
- [ ] Năm thiết lập securityContext, mỗi cái chặn kiểu tấn công nào?
- [ ] Vì sao `readOnlyRootFilesystem` cần thêm một emptyDir?
- [ ] Ba profile PSA. Namespace `kalapa` dùng cái nào?
- [ ] PSA từ chối pod thì bạn thấy gì ở `kubectl get deploy`? Tìm lý do ở đâu?
- [ ] Vì sao `automountServiceAccountToken: false`?
- [ ] Mặc định, pod A có gọi được pod B ở namespace khác không?
- [ ] Vì sao NetworkPolicy đầu tiên bạn viết lại là cái nguy hiểm?
- [ ] Vì sao policy trên minikube không có hiệu lực? Kiểm chứng kiểu gì?
- [ ] Ba cách quản lý secret trong GitOps, đánh đổi mỗi cách?
- [ ] Ba thứ hay hỏng khi verify JWT?
- [ ] Vì sao phải ghim thuật toán? Hai tấn công nào bị chặn?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Chạy container bằng root | Thoát container = root trên host |
| Không đặt securityContext | Mặc định của Kubernetes rất lỏng |
| Không bật PSA | securityContext thành tự nguyện, ai quên là lọt |
| Thấy Deployment tồn tại nên tưởng ổn | Pod có thể đang bị từ chối hàng loạt |
| Để token service account tự mount | Thông tin đăng nhập nằm sẵn chờ bị lấy |
| Không có NetworkPolicy | Một pod thủng là cả cluster bị quét |
| Viết NetworkPolicy mà quên DNS | Mọi lời gọi chết, log đổ lỗi sai chỗ |
| Tin policy chạy mà chưa kiểm chứng | CNI của minikube bỏ qua chúng |
| Commit secret thật | Git không quên; phải xoay khoá |
| Không ghim thuật toán JWT | `alg: none` và nhầm lẫn khoá HS256 |
| Nới kiểm tra issuer cho "đỡ phiền" | Chấp nhận token từ bất kỳ ai |
| Cài `curl`, `vim` vào image production | Chính là công cụ kẻ tấn công cần |

---

**Bài tiếp:** [15 — Học tiếp gì](15-hoc-tiep-gi.md)
