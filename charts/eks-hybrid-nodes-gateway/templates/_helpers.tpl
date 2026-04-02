{{/*
Chart name, truncated to 63 chars.
*/}}
{{- define "eks-hybrid-nodes-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, truncated to 63 chars.
*/}}
{{- define "eks-hybrid-nodes-gateway.fullname" -}}
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

{{/*
Chart label value.
*/}}
{{- define "eks-hybrid-nodes-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels following Helm best practices.
*/}}
{{- define "eks-hybrid-nodes-gateway.labels" -}}
helm.sh/chart: {{ include "eks-hybrid-nodes-gateway.chart" . }}
{{ include "eks-hybrid-nodes-gateway.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by Deployment and Service.
*/}}
{{- define "eks-hybrid-nodes-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "eks-hybrid-nodes-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "eks-hybrid-nodes-gateway.serviceAccountName" -}}
{{- include "eks-hybrid-nodes-gateway.fullname" . }}
{{- end }}

{{/*
Container image reference. Uses toString to handle numeric tags from --set.
*/}}
{{- define "eks-hybrid-nodes-gateway.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository (toString $tag) }}
{{- end }}

{{/*
Resource namespace. Uses .Values.namespace if set, otherwise .Release.Namespace.
*/}}
{{- define "eks-hybrid-nodes-gateway.namespace" -}}
{{- default .Release.Namespace .Values.namespace }}
{{- end }}
