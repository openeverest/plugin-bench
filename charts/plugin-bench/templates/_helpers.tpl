{{/* Expand the name of the chart. */}}
{{- define "plugin-bench.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Create a default fully qualified app name. */}}
{{- define "plugin-bench.fullname" -}}
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

{{/* Namespace for benchmark workload resources. */}}
{{- define "plugin-bench.workloadNamespace" -}}
{{- default .Release.Namespace .Values.workload.namespace -}}
{{- end }}

{{/* ServiceAccount used by the coordinator deployment. */}}
{{- define "plugin-bench.coordinatorServiceAccountName" -}}
{{- printf "%s-coordinator" (include "plugin-bench.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* ServiceAccount used by future benchmark Jobs. */}}
{{- define "plugin-bench.runnerServiceAccountName" -}}
{{- printf "%s-runner" (include "plugin-bench.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* Role used by the coordinator for benchmark workloads. */}}
{{- define "plugin-bench.workloadRoleName" -}}
{{- printf "%s-workload" (include "plugin-bench.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* RoleBinding used to grant the coordinator workload permissions. */}}
{{- define "plugin-bench.workloadRoleBindingName" -}}
{{- printf "%s-binding" (include "plugin-bench.workloadRoleName" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* Chart label. */}}
{{- define "plugin-bench.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels. */}}
{{- define "plugin-bench.labels" -}}
helm.sh/chart: {{ include "plugin-bench.chart" . }}
{{ include "plugin-bench.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels. */}}
{{- define "plugin-bench.selectorLabels" -}}
app.kubernetes.io/name: {{ include "plugin-bench.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* Resolve driver images before passing one JSON configuration to startup.
     Legacy image fields apply only to pgbench; explicit driver fields win.
     Do not use default on enabled: false must remain an explicit disablement. */}}
{{- define "plugin-bench.driverConfig" -}}
{{- $drivers := dict -}}
{{- range $id, $config := .Values.drivers -}}
  {{- if not (kindIs "map" $config) -}}
    {{- fail (printf "drivers.%s must be an object" $id) -}}
  {{- end -}}
  {{- if not (kindIs "bool" $config.enabled) -}}
    {{- fail (printf "drivers.%s.enabled must be a boolean" $id) -}}
  {{- end -}}
  {{- $entry := dict "enabled" $config.enabled -}}
  {{- if $config.enabled -}}
    {{- $image := $config.image | default dict -}}
    {{- $legacy := dict -}}
    {{- if eq $id "pgbench" -}}
      {{- $legacy = $.Values.runner.image | default dict -}}
    {{- end -}}
    {{- $repository := $image.repository | default $legacy.repository | default (printf "ghcr.io/openeverest/plugin-bench-%s" $id) -}}
    {{- $tag := $image.tag | default $legacy.tag | default $.Chart.AppVersion -}}
    {{- $policy := $image.pullPolicy | default $legacy.pullPolicy | default "IfNotPresent" -}}
    {{- $_ := set $entry "image" (printf "%s:%s" $repository $tag) -}}
    {{- $_ := set $entry "imagePullPolicy" $policy -}}
  {{- end -}}
  {{- $_ := set $drivers $id $entry -}}
{{- end -}}
{{- $drivers | toJson -}}
{{- end -}}
