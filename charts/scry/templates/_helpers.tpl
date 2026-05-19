{{/*
Common labels + name resolvers. Standard helm template idiom — let
nameOverride / fullnameOverride from values.yaml customise without
forking the chart.
*/}}

{{- define "scry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "scry.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "scry.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "scry.labels" -}}
helm.sh/chart: {{ include "scry.chart" . }}
{{ include "scry.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "scry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "scry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "scry.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{- define "scry.secretName" -}}
{{- if .Values.existingSecretName -}}
{{- .Values.existingSecretName -}}
{{- else -}}
{{- printf "%s-config" (include "scry.fullname" .) -}}
{{- end -}}
{{- end -}}
