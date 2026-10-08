# 06 — ConfigMap và Secret

> **Bài này trả lời:** Cấu hình để đâu? Mật khẩu để đâu? Đổi cấu hình rồi thì
> ứng dụng có tự biết không?
>
> **Cần xong bài:** [05](05-service-dns-ingress.md)

---

## 1. Lý thuyết

### 1.1 Vì sao không nhét cấu hình vào image

Nếu `config.yaml` nằm trong image thì mỗi môi trường cần một image khác nhau.
Mà image khác nhau nghĩa là **thứ bạn test không phải thứ bạn chạy ở
production**. Toàn bộ giá trị của container biến mất.

Nguyên tắc (lấy từ [12-factor app](https://12factor.net/config)):

> **Một image, nhiều môi trường.** Mọi thứ khác nhau giữa các môi trường phải
> đến từ bên ngoài lúc chạy.

### 1.2 ConfigMap

ConfigMap là một bản đồ khoá-giá trị chứa dữ liệu **không nhạy cảm**.

```yaml
kind: ConfigMap
metadata:
  name: kalapa-config
data:
  config.yaml: |
    server:
      httpPort: 8080
```

Hai cách đưa vào pod:

**Cách A — biến môi trường**

```yaml
envFrom:
  - configMapRef:
      name: kalapa-config
```

Đơn giản, nhưng: chỉ phẳng (không lồng nhau), và **đọc một lần lúc khởi
động**. Đổi ConfigMap thì process không hay biết.

**Cách B — mount thành file**

```yaml
volumeMounts:
  - name: cfg
    mountPath: /etc/kalapa
```

Hỗ trợ YAML lồng nhau, và kubelet **tự cập nhật file** khi ConfigMap đổi
(trong vòng ~1 phút). Nhưng — xem mục 1.4 — ứng dụng vẫn phải tự đọc lại.

> Repo này dùng cách B cho cấu hình (YAML lồng nhau) và cách A cho mật khẩu
> (Secret → biến môi trường).

### 1.3 Secret

Secret trông y hệt ConfigMap nhưng dành cho dữ liệu nhạy cảm.

**Sự thật khó chịu nhất về Secret:** nội dung chỉ được **base64**, không phải
mã hoá.

```bash
echo 'bWF0a2hhdQ==' | base64 -d     # ra ngay mật khẩu
```

Vậy Secret hơn ConfigMap ở điểm nào?

| Điểm | Lợi ích thật |
|---|---|
| Phân quyền RBAC riêng | Cấp quyền đọc ConfigMap mà không cấp quyền đọc Secret |
| Không hiện trong `kubectl describe` | Giảm rò rỉ do vô ý khi chia sẻ màn hình, dán log |
| Không nằm trong `helm get values` | Cùng lý do |
| Chỉ gửi tới node thật sự cần | Giảm bề mặt phơi bày |
| Có thể bật mã hoá tại chỗ trong etcd | Cấu hình ở phía apiserver |
| Mount bằng tmpfs (RAM) | Không chạm đĩa của node |

Tóm lại: Secret **giảm số nơi bí mật vô tình xuất hiện**, chứ không làm nó bí
mật về mặt mật mã học.

> Repo này commit mật khẩu dev dạng chữ rõ **có chủ ý**, vì chúng là
> `kalapa-dev-password` trên laptop và vì giấu đi sẽ giấu luôn cái luồng cần
> học. Với hệ thống thật thì xem [bài 14](14-bao-mat.md).

### 1.4 Vấn đề lớn: đổi cấu hình rồi thì sao?

Đây là chỗ hầu hết người mới hiểu sai.

```
Bạn sửa ConfigMap
      │
      ▼
kubelet cập nhật file trong pod  (~1 phút)   ✓ tự động
      │
      ▼
Process ĐỌC LẠI file?                        ✗ KHÔNG
```

Hầu hết ứng dụng đọc cấu hình **một lần lúc khởi động**. File trên đĩa đã đổi,
nhưng giá trị trong bộ nhớ vẫn là giá trị cũ. Và với biến môi trường thì còn
tệ hơn: biến môi trường của một process **không thể đổi** sau khi process đã
chạy, kể cả về nguyên tắc.

Ba cách giải quyết:

| Cách | Hoạt động thế nào | Đánh đổi |
|---|---|---|
| **Hot reload trong app** | App tự theo dõi file và nạp lại | Code phức tạp, dễ sai trạng thái nửa chừng |
| **Reloader** | Một controller theo dõi ConfigMap/Secret và restart Deployment | Đơn giản, đúng đắn — mất vài giây rolling restart |
| **Checksum trong pod template** | Băm nội dung config vào annotation của pod → nội dung đổi → template đổi → Deployment tự roll | Chỉ hoạt động khi cấu hình đi qua Helm |

Repo này dùng **cả hai cách sau**. Vì sao cả hai: Reloader xử lý trường hợp
ConfigMap bị sửa trực tiếp; checksum xử lý trường hợp `helm upgrade`. Một thay
đổi cấu hình âm thầm không có hiệu lực là loại lỗi tệ nhất, nên đáng để có hai
lớp bảo hiểm.

### 1.5 Cấu hình phân tầng

Hệ thống thật không có một nguồn cấu hình duy nhất. Thường là:

```
giá trị mặc định (biên dịch sẵn)
      ↓ bị ghi đè bởi
file cấu hình chung    (ConfigMap dùng chung)
      ↓ bị ghi đè bởi
file cấu hình riêng    (ConfigMap của từng service)
      ↓ bị ghi đè bởi
biến môi trường        (Secret, giá trị theo môi trường)
```

Tầng sau thắng tầng trước. Lợi ích: thứ gì giống nhau khai báo một lần; thứ gì
khác nhau khai báo đúng ở chỗ nó khác; và **mật khẩu chỉ đi qua tầng cuối**,
nơi ít lộ nhất.

---

## 2. Trong repo này nằm đâu

### Chart chứa toàn bộ cấu hình

`deploy/charts/kalapa-config/` — tương đương `yas-configuration` của YAS.

### ConfigMap dùng chung

`deploy/charts/kalapa-config/templates/configmap.yaml:8`

```yaml
kind: ConfigMap
metadata:
  name: kalapa-config
  annotations:
    reloader.stakater.com/match: "true"
data:
  config.yaml: |
    {{- toYaml .Values.config | nindent 4 }}
```

`toYaml` biến toàn bộ khối `config:` trong `values.yaml` thành chuỗi YAML. Nhờ
vậy cấu trúc cấu hình được viết một lần ở `values.yaml` thay vì lặp lại trong
template.

### ConfigMap riêng từng service — sinh bằng vòng lặp

Cùng file, dòng 21:

```yaml
{{- range $name, $overlay := .Values.serviceConfig }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: kalapa-{{ $name }}-config
data:
  {{ $name }}.yaml: |
    {{- toYaml $overlay | nindent 4 }}
{{- end }}
```

Thêm một service chỉ cần thêm một khối trong `serviceConfig:` ở `values.yaml`
— không phải viết thêm template nào.

### Secret

`deploy/charts/kalapa-config/templates/secret.yaml:14`

```yaml
kind: Secret
metadata:
  name: kalapa-postgres-credentials
type: Opaque
stringData:
  POSTGRES_USERNAME: {{ .Values.credentials.postgres.username | quote }}
  POSTGRES_PASSWORD: {{ .Values.credentials.postgres.password | quote }}
```

> **`stringData` chứ không phải `data`.** Với `data` bạn phải tự base64 mọi
> giá trị, và một lỗi đánh máy chỉ lộ ra khi pod không khởi động được.
> `stringData` để Kubernetes tự mã hoá.

### Pod nhận cấu hình thế nào

`deploy/charts/go-service/templates/deployment.yaml`, ba chỗ:

**Mount ConfigMap thành file** (phần `volumes` và `volumeMounts`):

```yaml
      volumes:
        - name: platform-config
          configMap:
            name: {{ .Values.configMapName }}      # kalapa-config
# ...
          volumeMounts:
            - name: platform-config
              mountPath: /etc/kalapa
              readOnly: true
```

**Secret thành biến môi trường** (dòng 93–99):

```yaml
          envFrom:
            {{- range .Values.envFromSecrets }}
            - secretRef:
                name: {{ . }}
            {{- end }}
```

**Chỉ cho app biết file nằm đâu** (phần `env`):

```yaml
            - name: CONFIG_FILE
              value: /etc/kalapa/config.yaml
            {{- if .Values.extraConfigMap }}
            - name: CONFIG_EXTRA_FILES
              value: /etc/kalapa/service/{{ .Values.extraConfigKey }}
            {{- end }}
```

### Code đọc cấu hình

`internal/platform/config/config.go:109`, hàm `Load`. Đây là nơi bốn tầng ở
mục 1.5 được hiện thực:

```go
func Load(serviceName string) (Config, error) {
	cfg := defaults()                                        // tầng 1
	cfg.Service.Name = serviceName

	files := []string{envOr("CONFIG_FILE", "/etc/kalapa/config.yaml")}  // tầng 2
	if extra := os.Getenv("CONFIG_EXTRA_FILES"); extra != "" {
		files = append(files, strings.Split(extra, ",")...)             // tầng 3
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue        // thiếu file không phải lỗi
		}
		...
		yaml.Unmarshal(raw, &cfg)
	}

	applyEnv(&cfg)          // tầng 4 — thắng tất cả
	...
}
```

Hai chi tiết thiết kế đáng học:

**File thiếu không gây lỗi.** Nhờ vậy `go run ./cmd/kyc` chạy được trên laptop
mà không cần ConfigMap nào, và lớp phủ tuỳ chọn thật sự là tuỳ chọn.

**`applyEnv` chạy cuối cùng** (`internal/platform/config/config.go:143`) — đó là cách tầng môi trường
luôn thắng.

Và mật khẩu được ghép vào DSN ở `internal/platform/config/config.go:180`:

```go
func (d Database) DSN() string {
	// URL (không nhạy cảm) đến từ ConfigMap
	// user/password (nhạy cảm) đến từ Secret qua biến môi trường
	...
}
```

> Đây là lý do `database.url` trong ConfigMap **không chứa mật khẩu**. Nửa
> không nhạy cảm nằm chỗ dễ đọc, nửa nhạy cảm nằm chỗ khó đọc.

### Hai lớp bảo hiểm cho việc đổi cấu hình

**Lớp 1 — Reloader.** Annotation ở `deployment.yaml` phần metadata:

```yaml
  annotations:
    reloader.stakater.com/search: "true"
```

Và phía ConfigMap/Secret là `reloader.stakater.com/match: "true"`. Reloader chỉ
để ý những object có nhãn đó, nên một ConfigMap không liên quan bị sửa sẽ
không gây restart hàng loạt.

Reloader được cài qua `deploy/argocd/platform/03-reloader.yaml`.

**Lớp 2 — checksum.** Trong pod template:

```yaml
    metadata:
      annotations:
        checksum/config: {{ include "go-service.configChecksum" . }}
```

Hàm này ở `_helpers.tpl`:

```
{{- define "go-service.configChecksum" -}}
{{- toYaml .Values.config | sha256sum }}
{{- end }}
```

Nội dung config đổi → băm đổi → pod template đổi → Deployment thấy template
khác → tự động rolling update.

---

## 3. Thực hành

### 3.1 Nhìn vào ConfigMap thật

```bash
kubectl -n kalapa get cm
kubectl -n kalapa get cm kalapa-config -o yaml | yq '.data."config.yaml"'
```

So sánh output đó với `deploy/charts/kalapa-config/values.yaml` khối `config:`.
Chúng phải khớp.

### 3.2 Nhìn file bên trong pod

Image là distroless nên không có `cat`. Dùng `kubectl debug` để gắn thêm một
container tạm vào cùng pod:

```bash
POD=$(kubectl -n kalapa get pod -l app.kubernetes.io/name=kyc -o name | head -1)
kubectl -n kalapa debug $POD -it --image=busybox:1.36 --target=kyc -- sh
```

Trong shell:

```sh
ls -la /proc/1/root/etc/kalapa/
cat /proc/1/root/etc/kalapa/config.yaml
cat /proc/1/root/etc/kalapa/service/kyc.yaml
exit
```

> Đây là kỹ thuật debug rất đáng thuộc: **ephemeral container**. Nó cho bạn
> shell trong không gian của pod mà không cần nhét shell vào image production.

### 3.3 Secret chỉ là base64

```bash
kubectl -n kalapa get secret kalapa-postgres-credentials -o yaml
kubectl -n kalapa get secret kalapa-postgres-credentials \
  -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d; echo
```

Mật khẩu hiện ra ngay. Hãy để điều này đọng lại.

Giờ xem điểm khác biệt thật sự:

```bash
kubectl -n kalapa describe secret kalapa-postgres-credentials   # chỉ thấy số byte
kubectl -n kalapa describe cm kalapa-config | head -20          # thấy toàn bộ nội dung
```

### 3.4 Chứng kiến Reloader làm việc

```bash
# Terminal 1 — theo dõi
kubectl -n kalapa get pods -l app.kubernetes.io/name=kyc -w

# Terminal 2 — xem log của Reloader
kubectl -n kalapa logs -f deploy/kalapa-reloader
```

Terminal 3 — sửa ConfigMap:

```bash
kubectl -n kalapa patch cm kalapa-kyc-config --type merge \
  -p '{"data":{"kyc.yaml":"kafka:\n  topic: kyc.application.submitted\nservice:\n  logLevel: debug\n"}}'
```

Trong vài giây, log Reloader sẽ báo nó phát hiện thay đổi, và terminal 1 cho
thấy một rolling restart. Pod mới khởi động với `logLevel: debug`.

Kiểm chứng:

```bash
kubectl -n kalapa logs deploy/kyc --tail=5
```

Khôi phục bằng cách đồng bộ lại từ Git (nếu dùng Argo CD) hoặc:

```bash
helm upgrade --install kalapa-config ./deploy/charts/kalapa-config -n kalapa
```

### 3.5 Chứng minh biến môi trường KHÔNG tự cập nhật

```bash
# Xem biến môi trường hiện tại của process
POD=$(kubectl -n kalapa get pod -l app.kubernetes.io/name=kyc -o name | head -1)
kubectl -n kalapa debug $POD -it --image=busybox:1.36 --target=kyc -- \
  sh -c 'tr "\0" "\n" < /proc/1/environ | grep POSTGRES'
```

Sửa Secret rồi kiểm tra lại **mà không** restart pod — giá trị cũ vẫn nguyên.
Biến môi trường của một process đang chạy không thể đổi, đó là giới hạn của hệ
điều hành chứ không phải của Kubernetes.

### 3.6 Thấy checksum đổi

```bash
kubectl -n kalapa get deploy kyc \
  -o jsonpath='{.spec.template.metadata.annotations.checksum/config}'; echo
```

Sửa bất kỳ giá trị nào trong khối `config:` của
`deploy/charts/kalapa-config/values.yaml`, chạy `make render`, rồi so lại —
băm đã khác.

---

## 4. Tự kiểm

- [ ] Vì sao không nhét `config.yaml` vào image?
- [ ] Hai cách đưa ConfigMap vào pod, mỗi cách ưu nhược gì?
- [ ] Secret có được mã hoá không? Vậy nó hơn ConfigMap ở đâu?
- [ ] Sửa ConfigMap thì file trong pod có đổi không? Ứng dụng có biết không?
- [ ] Vì sao biến môi trường không bao giờ tự cập nhật?
- [ ] Reloader hoạt động thế nào? Hai annotation nào cần có?
- [ ] Checksum trong pod template giải quyết trường hợp nào mà Reloader không?
- [ ] Bốn tầng cấu hình trong repo này, tầng nào thắng?
- [ ] Vì sao `database.url` trong ConfigMap không chứa mật khẩu?
- [ ] `stringData` khác `data` thế nào?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Tưởng Secret được mã hoá | Chỉ base64. Ai đọc được Secret là đọc được mật khẩu |
| Sửa ConfigMap rồi tưởng app đã nhận | Cần Reloader hoặc restart. Không có thì cấu hình "có mà không có" |
| Nhét mật khẩu vào ConfigMap | Hiện trong `describe`, `helm get values`, giao diện Argo CD |
| `kubectl create secret` bằng tay | Không ai tái tạo được. Dựng lại cluster là mất |
| Dùng `data:` rồi base64 thủ công | Sai một ký tự chỉ lộ ra lúc pod chết |
| Một ConfigMap khổng lồ cho mọi thứ | Sửa một giá trị → restart mọi service |
| Commit mật khẩu thật vào git | Git không quên. Phải xoay khoá chứ không phải xoá commit |

---

**Bài tiếp:** [07 — Tài nguyên, probe, vòng đời](07-tai-nguyen-probe-vong-doi.md)
