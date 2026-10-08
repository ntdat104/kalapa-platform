# 08 — Helm

> **Bài này trả lời:** 3 service × 8 loại object = 24 file YAML gần như giống
> hệt nhau. Làm sao sống nổi? Và vì sao template lại nguy hiểm nếu dùng sai?
>
> **Cần xong bài:** [07](07-tai-nguyen-probe-vong-doi.md)

---

## 1. Lý thuyết

### 1.1 Vấn đề

Ba service của repo này, mỗi service cần: Deployment, Service, ServiceAccount,
Ingress, HPA, PDB, ServiceMonitor, NetworkPolicy. Là 24 file. Và 23 trong số
đó khác nhau đúng vài dòng.

Sửa cách đặt probe? Sửa 3 chỗ. Thêm một label chuẩn? Sửa 24 chỗ. Sớm muộn
chúng sẽ lệch nhau, và lúc đó bạn có một service hành xử khác hai service kia
mà không ai nhớ vì sao.

### 1.2 Helm là gì

Helm là **trình quản lý gói cho Kubernetes**. Ba khái niệm:

| Từ | Là gì |
|---|---|
| **Chart** | Một gói: template + giá trị mặc định + metadata |
| **Values** | Tham số đưa vào template |
| **Release** | Một lần cài chart vào cluster, có tên và có lịch sử phiên bản |

Cơ chế rất đơn giản:

```
template (Go template)  +  values.yaml  ──render──►  YAML thuần  ──apply──►  cluster
```

Helm **không** chạy ở trong cluster như một controller. Nó render ra YAML rồi
gửi tới apiserver, y như `kubectl apply`. Phần "thông minh" duy nhất là nó lưu
lại lịch sử các bản render để `helm rollback` hoạt động.

### 1.3 Cấu trúc một chart

```
mychart/
├── Chart.yaml          tên, phiên bản, dependency
├── values.yaml         giá trị mặc định
├── templates/
│   ├── _helpers.tpl    hàm dùng lại (tên bắt đầu bằng _ -> không render ra object)
│   ├── deployment.yaml
│   └── service.yaml
└── charts/             dependency đã tải về
```

### 1.4 Cú pháp cần biết

```yaml
# Đọc một giá trị
replicas: {{ .Values.replicaCount }}

# Giá trị mặc định khi rỗng
tag: {{ .Values.image.tag | default .Chart.AppVersion }}

# Bọc trong dấu nháy (BẮT BUỘC với số hoặc chuỗi có thể bị hiểu nhầm)
value: {{ .Values.port | quote }}

# Điều kiện
{{- if .Values.ingress.enabled }}
# ...
{{- end }}

# Điều kiện kèm gán biến ngữ cảnh — chỉ render khi có giá trị
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 8 }}
{{- end }}

# Vòng lặp
{{- range .Values.envFromSecrets }}
- secretRef:
    name: {{ . }}
{{- end }}

# Gọi hàm định nghĩa trong _helpers.tpl
{{- include "go-service.labels" . | nindent 4 }}
```

**Dấu `-` trong `{{-`** xoá khoảng trắng phía trước. Trong YAML, thụt lề là cú
pháp, nên thiếu một dấu `-` là file render ra sai hoàn toàn. Đây là thứ gây khó
chịu nhất khi mới học Helm.

**`nindent N`** = xuống dòng + thụt N khoảng trắng. Dùng khi chèn một khối
nhiều dòng vào giữa file.

### 1.5 Library chart và dependency — ý tưởng then chốt

Thay vì 3 chart giống nhau, ta làm:

```
go-service/              chart GỐC: chứa toàn bộ template
    ↑        ↑        ↑
    │        │        │   mỗi chart dưới đây chỉ có values.yaml
gateway/   kyc/   scoring/
```

Chart con khai báo chart gốc là dependency rồi **chỉ ghi đè values**. Không có
template nào trong chart con.

Kết quả: sửa cách đặt probe là sửa **một** file. Mọi service nhận thay đổi đó.

### 1.6 Ba cái bẫy của Helm

**Bẫy 1 — số lớn biến thành ký hiệu khoa học.**

```yaml
# values.yaml
segmentBytes: 10485760
```

```yaml
# template
segment.bytes: {{ .Values.topic.segmentBytes | quote }}
```

Render ra: `segment.bytes: "1.048576e+07"`. Helm đọc số không dấu nháy thành
`float64`, và `quote` in nó ra dạng khoa học. Kafka từ chối giá trị này.

**Cách tránh:** bọc nháy ngay trong `values.yaml` cho mọi số lớn hơn ~2²⁴.

> Đây không phải ví dụ giả định — nó đã thực sự xảy ra khi dựng repo này, và
> triệu chứng là KafkaTopic kẹt ở `Ready=False` với lý do chỉ nằm trong
> `.status`. Chú thích cảnh báo hiện nằm ở
> `deploy/charts/kafka/values.yaml`, ngay trên `retentionMs`.

**Bẫy 2 — `helm lint` không bắt được lỗi logic.** `lint` chỉ kiểm tra cấu
trúc. Một template tham chiếu value không tồn tại chỉ lộ ra khi `helm template`
hoặc khi apply. Luôn chạy cả hai.

**Bẫy 3 — chart hoá mọi thứ.** Template quá nhiều thì không ai đọc nổi file
render ra. Quy tắc: tham số hoá thứ **thật sự khác nhau giữa các lần cài**, còn
lại viết cứng.

---

## 2. Trong repo này nằm đâu

### Chart gốc

`deploy/charts/go-service/` — đọc `Chart.yaml` trước:

```yaml
apiVersion: v2
name: go-service
description: |
  Base chart for every Kalapa Go microservice...
type: application
version: 0.1.0
```

### Chart con

`deploy/charts/kyc/Chart.yaml`:

```yaml
dependencies:
  - name: go-service
    version: 0.1.0
    repository: file://../go-service
```

`file://` nghĩa là chart gốc nằm **trong cùng repo**, không phải tải từ
registry. Lợi ích: Argo CD giải quyết dependency từ chính commit nó đang đồng
bộ, nên chart và ứng dụng không bao giờ lệch phiên bản.

Và `deploy/charts/kyc/values.yaml` — **toàn bộ chart con chỉ có file này**:

```yaml
go-service:            # <- khoá này phải trùng TÊN dependency
  nameOverride: kyc
  fullnameOverride: kyc
  image:
    repository: ghcr.io/kalapa-lab/kalapa-kyc
    tag: latest
  extraConfigMap: kalapa-kyc-config
  envFromSecrets:
    - kalapa-postgres-credentials
  ingress:
    enabled: false
```

> Khoá lồng `go-service:` là quy tắc của Helm: muốn đặt value cho subchart thì
> phải lồng dưới tên subchart. Quên lồng là values bị bỏ qua **không báo lỗi**
> — chart vẫn render, chỉ là render bằng giá trị mặc định.

### Helpers — nơi nhãn được định nghĩa

`deploy/charts/go-service/templates/_helpers.tpl`. Bốn hàm đáng đọc:

```
{{- define "go-service.fullname" -}}     tên object, cắt ở 63 ký tự (giới hạn DNS)
{{- define "go-service.labels" -}}       nhãn ĐẦY ĐỦ (có version, chart, managed-by)
{{- define "go-service.selectorLabels" -}}  nhãn TỐI THIỂU (chỉ name + instance)
{{- define "go-service.configChecksum" -}}  băm của config, cho pod annotation
```

Việc tách `labels` và `selectorLabels` chính là cách chart tránh bẫy "selector
bất biến" ở bài 04.

### Dùng lại trong template

`deployment.yaml`:

```yaml
metadata:
  name: {{ include "go-service.fullname" . }}
  labels:
    {{- include "go-service.labels" . | nindent 4 }}
spec:
  selector:
    matchLabels:
      {{- include "go-service.selectorLabels" . | nindent 6 }}
```

Ba hàm, dùng ở cả `service.yaml`, `ingress.yaml`, `hpa.yaml`,
`servicemonitor.yaml`… Đổi quy ước đặt nhãn là sửa một chỗ.

### Cửa thoát hiểm

`deploy/charts/go-service/templates/extra-manifests.yaml`:

```yaml
{{- range .Values.extraObjects }}
---
{{- if typeIs "string" . }}
{{- tpl . $ }}
{{- else }}
{{- tpl (toYaml .) $ }}
{{- end }}
{{- end }}
```

Cho phép một service thêm object lạ (một Job, một ExternalSecret) mà không
phải fork chart gốc. `tpl` cho phép chính giá trị đó chứa biểu thức template.

### Chart cấu hình dùng vòng lặp

`deploy/charts/kalapa-config/templates/configmap.yaml:21` — đã xem ở bài 06.
Đây là ví dụ đẹp nhất về `range` trong repo: thêm service chỉ cần thêm một
khối values.

---

## 3. Thực hành

### 3.1 Nhìn thấy template biến thành YAML

```bash
cd ~/Documents/resource/kalapa-platform
helm dependency build ./deploy/charts/kyc
helm template kyc ./deploy/charts/kyc -n kalapa | head -60
```

Đây **chính xác** là thứ sẽ được gửi tới apiserver. Không có gì ẩn.

```bash
# Chỉ xem Deployment
helm template kyc ./deploy/charts/kyc -n kalapa | yq 'select(.kind == "Deployment")'
```

### 3.2 So sánh hai service để thấy giá trị của chart gốc

```bash
helm template kyc ./deploy/charts/kyc -n kalapa > /tmp/kyc.yaml
helm template gateway ./deploy/charts/gateway -n kalapa > /tmp/gateway.yaml
diff <(yq 'select(.kind=="Deployment")' /tmp/kyc.yaml) \
     <(yq 'select(.kind=="Deployment")' /tmp/gateway.yaml)
```

Khác biệt rất ít — và đó là toàn bộ giá trị: 2 file values thay vì 2 bộ
template.

### 3.3 Nghịch với values

```bash
helm template kyc ./deploy/charts/kyc -n kalapa \
  --set go-service.replicaCount=5 \
  --set go-service.resources.limits.memory=256Mi \
  | yq 'select(.kind=="Deployment") | {"replicas": .spec.replicas, "mem": .spec.template.spec.containers[0].resources.limits.memory}'
```

Giờ thử **quên khoá lồng** — lỗi im lặng kinh điển:

```bash
helm template kyc ./deploy/charts/kyc -n kalapa --set replicaCount=5 \
  | yq 'select(.kind=="Deployment") | .spec.replicas'
```

Kết quả vẫn là `1`. Không có cảnh báo nào. Hãy nhớ cảm giác này.

### 3.4 Tự tay tạo bẫy số lớn

```bash
cat > /tmp/thu-so.yaml <<'EOF'
soLon: 10485760
EOF
cat > /tmp/thu-so.tpl <<'EOF'
khongNhay: {{ .Values.soLon }}
coNhay: {{ .Values.soLon | quote }}
EOF
mkdir -p /tmp/chart-thu/templates && cp /tmp/thu-so.yaml /tmp/chart-thu/values.yaml
cp /tmp/thu-so.tpl /tmp/chart-thu/templates/thu.yaml
cat > /tmp/chart-thu/Chart.yaml <<'EOF'
apiVersion: v2
name: chart-thu
version: 0.1.0
EOF
helm template t /tmp/chart-thu
```

Bạn sẽ thấy `coNhay: "1.048576e+07"`. Giờ sửa `values.yaml` thành
`soLon: "10485760"` rồi chạy lại — đúng ngay.

### 3.5 Lỗi khoảng trắng

```bash
# Xem template gốc
sed -n '20,32p' deploy/charts/go-service/templates/deployment.yaml
```

Thử bỏ một dấu `-`:

```bash
cp deploy/charts/go-service/templates/hpa.yaml /tmp/hpa.bak
sed -i.tmp 's/{{- if .Values.autoscaling.enabled }}/{{ if .Values.autoscaling.enabled }}/' \
  deploy/charts/go-service/templates/hpa.yaml
helm template kyc ./deploy/charts/kyc -n kalapa --set go-service.autoscaling.enabled=true 2>&1 | head -5
# Khôi phục
cp /tmp/hpa.bak deploy/charts/go-service/templates/hpa.yaml
rm -f deploy/charts/go-service/templates/hpa.yaml.tmp
```

Dòng trống thừa ở đầu file có thể làm YAML hỏng. Đó là lý do `{{-` xuất hiện
khắp nơi.

### 3.6 Vòng đời một release

```bash
helm list -A --kube-context kalapa
helm history kyc -n kalapa --kube-context kalapa
helm get values kyc -n kalapa --kube-context kalapa
helm get manifest kyc -n kalapa --kube-context kalapa | head -20
```

`helm get manifest` cho bạn thấy chính xác thứ đang chạy. Rất hữu ích khi nghi
ngờ cluster không khớp với Git.

### 3.7 Lint và render — hai việc khác nhau

```bash
make lint      # helm lint cho mọi chart
make render    # helm template mọi chart ra /tmp/kalapa-rendered
ls -la /tmp/kalapa-rendered/
```

---

## 4. Tự kiểm

- [ ] Chart, values, release khác nhau thế nào?
- [ ] Helm có chạy trong cluster không? Nó thật ra làm gì?
- [ ] `{{-` khác `{{` ở đâu? Vì sao điều đó quan trọng với YAML?
- [ ] `nindent` làm gì?
- [ ] `_helpers.tpl` khác file template khác ở điểm nào?
- [ ] Vì sao `labels` và `selectorLabels` phải là hai hàm riêng?
- [ ] Trong `deploy/charts/kyc/values.yaml`, vì sao mọi thứ lồng dưới `go-service:`?
- [ ] Quên khoá lồng đó thì chuyện gì xảy ra?
- [ ] `file://` trong dependency có lợi gì so với repo chart?
- [ ] `helm lint` bắt được gì và không bắt được gì?
- [ ] Vì sao số lớn trong `values.yaml` phải bọc nháy?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Quên lồng values dưới tên subchart | Bị bỏ qua, không báo lỗi, chart dùng mặc định |
| Số lớn không bọc nháy | Render ra ký hiệu khoa học, thành phần đích từ chối |
| Chỉ chạy `helm lint` | Không bắt được value thiếu. Phải `helm template` nữa |
| Thiếu `{{-` | Dòng trống thừa làm YAML hỏng ở chỗ khó đoán |
| Tham số hoá mọi thứ | Không ai đọc nổi template, không ai dám sửa |
| Copy chart cho service mới | Quay lại đúng vấn đề mà Helm sinh ra để giải |
| `helm install` bằng tay ở production | Mệnh lệnh, không tái lập được (bài 12 sửa chuyện này) |
| Quên `helm dependency build` | `found in Chart.yaml, but missing in charts/ directory` |

---

**Bài tiếp:** [09 — Database và Operator](09-database-va-operator.md)
