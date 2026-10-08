{{/*
Chart name, overridable so a release named `kyc` renders objects named `kyc`
rather than `kyc-go-service`.
*/}}
{{- define "go-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified name. Truncated at 63 chars because that is the DNS label
limit, and a Service name longer than that is rejected by the API server.
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
Common labels: everything that is useful for querying but NOT part of the
selector. Putting a version label in the selector would make every upgrade a
rename, and Deployment selectors are immutable.
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
Selector labels: the immutable subset. Changing these on an existing release
requires deleting the Deployment first.
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
Checksum of the values that end up in the mounted config. Adding it to the pod
template annotations makes `helm upgrade` roll the Deployment whenever the
config changes — the belt to Reloader's braces, and the only mechanism that
works when Reloader is not installed.
*/}}
{{- define "go-service.configChecksum" -}}
{{- toYaml .Values.config | sha256sum }}
{{- end }}
