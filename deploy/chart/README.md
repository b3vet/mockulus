# mockulus Helm chart

```sh
helm install mockulus deploy/chart
```

That is the zero-config start: replicas serving from memory, no Couchbase, no
persistence. Point a WireMock client at the service and it works.

## Turning on persistence and clustering

```sh
helm install mockulus deploy/chart \
  --set couchbase.connstr=couchbase://cb.mockulus.svc \
  --set couchbase.existingSecret=mockulus-couchbase
```

Stubs then live in Couchbase and survive pod restarts, and a stub registered
through any replica propagates to the others within `config.syncInterval`.

## Scaling

CPU-based autoscaling fits CPU-bound mock traffic:

```sh
--set autoscaling.enabled=true --set autoscaling.maxReplicas=10
```

To scale on latency or throughput instead, run Prometheus Adapter over
`mockulus_http_request_duration_seconds` and pass your own rule through
`autoscaling.metrics`; the shape is in the comments of `values.yaml`.

Replica count is otherwise free: any pod serves any request and any admin call.
Couchbase load does not grow with it — each pod is one epoch read per
`syncInterval`, and scenario and journal load track feature use, not pod count.

**Single replica with the memory store is the WireMock drop-in mode**, and the
migration on-ramp: no Couchbase at all, no propagation delay, nothing to
operate.

## Hardening

The admin API is open by default, which assumes the deployment is in-cluster
only. When the mock port is reachable more broadly, use the hardened preset —
it takes the admin API off the mock port, requires a token, and adds a
NetworkPolicy around the ops port:

```sh
helm install mockulus deploy/chart \
  -f deploy/chart/values-hardened.yaml \
  --set adminAuth.existingSecret=mockulus-admin-token
```

## Tracing

Off by default (SPEC §14.3), and off it costs a served request one atomic load,
so the performance targets are the ones measured without it. Point it at a
collector to turn it on:

```sh
helm upgrade --install mockulus deploy/chart \
  --set tracing.enabled=true \
  --set tracing.endpoint=otel-collector.observability.svc:4318
```

The endpoint is `host:port` with no scheme — `tracing.insecure` decides that,
and defaults to cleartext for the in-cluster collector that is the usual case.
Enabling tracing without an endpoint fails at render rather than at rollout.

A hosted backend's ingestion token goes in `tracing.headers` and is a
credential, so it is delivered by Secret and `secretKeyRef` rather than as a
literal in the pod spec. Prefer `tracing.existingSecret`: a token passed as a
value is a token in the release history, the same objection the section above
raises about `adminAuth.token`.

## Isolation between teams

One deployment is one namespace for stubs, scenarios and the journal. Teams
sharing a deployment must keep their stubs distinguishable and must not call the
global resets (`POST /__admin/reset`, `DELETE /__admin/mappings`), which destroy
everyone's stubs. Teams that need real isolation get their own release — that is
deliberate, and cheaper than in-app multi-tenancy.

## Values

See `values.yaml`; every `config.*` key maps to the corresponding `MOCKULUS_*`
environment variable documented in SPEC §13. Anything not surfaced as a named
value can be passed through `config.extraEnv`.
