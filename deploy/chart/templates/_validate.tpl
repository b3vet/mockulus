{{/* SPDX-License-Identifier: Apache-2.0 */}}

{{/*
Resolve the store driver the same way mockulus does at startup (SPEC §13):
`auto` becomes couchbase when a connection string is set, and memory otherwise.
*/}}
{{- define "mockulus.effectiveStore" -}}
{{- if eq .Values.config.store "auto" -}}
{{- if .Values.couchbase.connstr -}}couchbase{{- else -}}memory{{- end -}}
{{- else -}}
{{- .Values.config.store -}}
{{- end -}}
{{- end -}}

{{/*
The highest replica count this release can reach.
*/}}
{{- define "mockulus.maxReplicas" -}}
{{- if .Values.autoscaling.enabled -}}
{{- .Values.autoscaling.maxReplicas -}}
{{- else -}}
{{- .Values.replicaCount -}}
{{- end -}}
{{- end -}}

{{/*
Refuse a deployment whose replica count and store driver contradict each other.

The memory and file drivers keep stubs inside a single process (SPEC §7.1,
§15.4). Running several replicas on top of one of them looks like it works —
pods start, probes pass — but a stub registered through the Service lands on
exactly one pod, so requests fail on the others at a rate set by the load
balancer. That is far worse than refusing to install, so this fails loudly,
which is the same contract the admin API applies to unsupported stubs (P3).
*/}}
{{- define "mockulus.validate" -}}
{{- $store := include "mockulus.effectiveStore" . -}}
{{- $replicas := int (include "mockulus.maxReplicas" .) -}}
{{- if and (gt $replicas 1) (or (eq $store "memory") (eq $store "file")) -}}
{{- fail (printf `

  mockulus: %d replicas with the "%s" store is not a working deployment.

  The %s driver keeps stubs inside a single process, so a stub registered
  through the Service would be served by one pod and 404 on the others.

  Choose one:

    * one replica — the WireMock drop-in mode, and the migration on-ramp:
        --set replicaCount=1

    * a shared store, which is what makes replicas interchangeable:
        --set couchbase.connstr=couchbase://cb.mockulus.svc \
        --set couchbase.existingSecret=mockulus-couchbase

  See SPEC §15.4 and deploy/chart/README.md.
` $replicas $store $store) -}}
{{- end -}}
{{- if and .Values.couchbase.connstr (not .Values.couchbase.existingSecret) (not .Values.couchbase.username) -}}
{{- fail "mockulus: couchbase.connstr is set, so either couchbase.existingSecret or couchbase.username and couchbase.password are required" -}}
{{- end -}}
{{- if and .Values.adminAuth.required (not .Values.adminAuth.existingSecret) (not .Values.adminAuth.token) -}}
{{- fail `

  mockulus: adminAuth.required is set and no token was supplied.

  The hardened preset asks for one because the rest of it — the admin API off
  the mock port, a NetworkPolicy around the ops port — narrows who can reach
  the admin API without ever requiring a credential from them. Rendering
  without the token would produce a release that reads as locked down and has
  an open admin API, which is worse than no preset at all.

    --set adminAuth.existingSecret=mockulus-admin-token

  Prefer an existing Secret over adminAuth.token: a token passed as a value is
  a token in the release history.
` -}}
{{- end -}}
{{- end -}}
