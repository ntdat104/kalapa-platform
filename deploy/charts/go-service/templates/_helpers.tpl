{{/*
Tên chart, cho phép ghi đè để một release tên `kyc` sinh ra object tên `kyc`
thay vì `kyc-go-service`.
*/}}
{{- define "go-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Tên đầy đủ. Cắt ở 63 ký tự vì đó là giới hạn của một nhãn DNS, và API server sẽ
từ chối một Service có tên dài hơn thế.
*/}}
{{- define "go-service.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "go-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Nhãn chung: mọi thứ hữu ích khi truy vấn nhưng KHÔNG thuộc selector. Đưa nhãn
version vào selector sẽ biến mỗi lần nâng cấp thành một lần đổi tên, mà selector
của Deployment thì bất biến.
*/}}
{{- define "go-service.labels" -}}
helm.sh/chart: {{ include "go-service.chart" . }}
{{ include "go-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: kalapa
{{- end }}

{{/*
Nhãn selector: phần bất biến. Đổi chúng trên một release đang tồn tại thì phải
xoá Deployment đi trước đã.
*/}}
{{- define "go-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "go-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "go-service.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "go-service.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Mã băm của phần giá trị sẽ đi vào file cấu hình được mount. Đưa nó vào annotation
của pod template khiến `helm upgrade` tự roll lại Deployment mỗi khi cấu hình
đổi — lớp bảo hiểm thứ hai bên cạnh Reloader, và là cơ chế duy nhất còn hoạt
động khi không cài Reloader.
*/}}
{{- define "go-service.configChecksum" -}}
{{- toYaml .Values.config | sha256sum }}
{{- end }}
