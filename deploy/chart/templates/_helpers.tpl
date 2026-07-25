{{/* SPDX-License-Identifier: Apache-2.0 */}}

{{- define "mockulus.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mockulus.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "mockulus.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "mockulus.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "mockulus.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mockulus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "mockulus.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/* Name of the Secret holding Couchbase credentials, if any is needed. */}}
{{- define "mockulus.couchbaseSecret" -}}
{{- if .Values.couchbase.existingSecret -}}
{{- .Values.couchbase.existingSecret -}}
{{- else -}}
{{- printf "%s-couchbase" (include "mockulus.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "mockulus.adminAuthSecret" -}}
{{- if .Values.adminAuth.existingSecret -}}
{{- .Values.adminAuth.existingSecret -}}
{{- else -}}
{{- printf "%s-admin-auth" (include "mockulus.fullname" .) -}}
{{- end -}}
{{- end -}}
