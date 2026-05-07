{{/*
Expand the name of the chart.
*/}}
{{- define "dra-memory-driver.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "dra-memory-driver.fullname" -}}
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

{{/*
Allow the release namespace to be overridden for multi-namespace deployments in combined charts
*/}}
{{- define "dra-memory-driver.namespace" -}}
  {{- if .Values.namespaceOverride -}}
    {{- .Values.namespaceOverride -}}
  {{- else -}}
    {{- .Release.Namespace -}}
  {{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "dra-memory-driver.chart" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" $name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "dra-memory-driver.labels" -}}
helm.sh/chart: {{ include "dra-memory-driver.chart" . }}
{{ include "dra-memory-driver.templateLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Template labels
*/}}
{{- define "dra-memory-driver.templateLabels" -}}
app.kubernetes.io/name: {{ include "dra-memory-driver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Values.selectorLabelsOverride }}
{{ toYaml .Values.selectorLabelsOverride }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "dra-memory-driver.selectorLabels" -}}
{{- if .Values.selectorLabelsOverride -}}
{{ toYaml .Values.selectorLabelsOverride }}
{{- else -}}
{{ include "dra-memory-driver.templateLabels" . }}
{{- end }}
{{- end }}

{{/*
Full image name with tag
*/}}
{{- define "dra-memory-driver.fullimage" -}}
{{- .Values.image.repository -}}:{{- .Values.image.tag | default .Chart.AppVersion -}}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "dra-memory-driver.serviceAccountName" -}}
{{- $name := printf "%s-service-account" (include "dra-memory-driver.fullname" .) }}
{{- if .Values.serviceAccount.create }}
{{- default $name .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the service account to use for the webhook
*/}}
{{- define "dra-memory-driver.webhookServiceAccountName" -}}
{{- $name := printf "%s-webhook-service-account" (include "dra-memory-driver.fullname" .) }}
{{- if .Values.webhook.serviceAccount.create }}
{{- default $name .Values.webhook.serviceAccount.name }}
{{- else }}
{{- default "default-webhook" .Values.webhook.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Get the latest available resource.k8s.io API version
Returns the highest available version or empty string if none found
*/}}
{{- define "dra-memory-driver.resourceApiVersion" -}}
{{- if .Capabilities.APIVersions.Has "resource.k8s.io/v1" -}}
resource.k8s.io/v1
{{- else if .Capabilities.APIVersions.Has "resource.k8s.io/v1beta2" -}}
resource.k8s.io/v1beta2
{{- else if .Capabilities.APIVersions.Has "resource.k8s.io/v1beta1" -}}
resource.k8s.io/v1beta1
{{- else -}}
{{- end -}}
{{- end -}}

{{/*
The driver name.
*/}}
{{- define "dra-memory-driver.driverName" -}}
{{- if .Values.driverName -}}
{{- .Values.driverName -}}
{{- else if eq .Values.deviceProfile "mem" -}}
mem.example.com
{{- else -}}
{{ print .Values.deviceProfile ".example.com" }}
{{- end -}}
{{- end -}}
