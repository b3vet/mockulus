# Operating mockulus

This is the document for whoever gets paged. It covers the deployment shapes
mockulus supports and when each is right, what the chart will and will not let
you install, how Couchbase has to be set up, what readiness actually means here,
what a rolling restart does, what breaks when the store goes away, and which
metrics are worth waking someone up for.

Two things to hold in your head before anything else.

**There are two listeners.** The mock port (`8080`) carries stub traffic and —
unless you turn it off — the WireMock-compatible `/__admin` API, because
WireMock clients default to same-port admin calls. The ops port (`9090`) carries
`/__admin` plus `/healthz`, `/readyz`, `/metrics` and `/debug/pprof`. The
operational surface is never on the mock port:

```console
$ for p in /metrics /healthz /readyz /debug/pprof/; do
    printf "%-16s %s\n" "$p" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:19310$p)"
  done
/metrics         404
/healthz         404
/readyz          404
/debug/pprof/    404
```

**mockulus implements a subset of WireMock.** Everything outside it is refused
at registration time with a 422 or a 404 naming the offending fields and
pointing at [ROADMAP.md](../ROADMAP.md) — never accepted and quietly ignored.
That matters operationally because a stub your suite registered against
WireMock may not register here at all, and you will find out at deploy time
rather than at 3am. The subset is written out in
[compatibility.md](compatibility.md); the contract behind it is
[SPEC §5](../SPEC.md#5-compatibility-contract), and the deviations list is §5.5.

> **About the transcripts below.** Every command shown with a `$` prompt was run
> against a local instance and its output is verbatim. Those instances were
> started with `MOCKULUS_PORT` and `MOCKULUS_ADMIN_PORT` in the 19310–19325 range
> so several scenarios could run side by side, which is why the port numbers vary
> from block to block. In a cluster they are `8080` and `9090`.

---

## 1. Choosing a topology

| Topology | Store | Stubs survive a restart | Cross-replica propagation | Use it for |
|---|---|---|---|---|
| One replica | `memory` | No | n/a | The WireMock drop-in, and the migration on-ramp |
| N replicas | `couchbase` | Yes | ≤ `sync_interval` (1 s) | Shared environments, CI at scale, anything HPA touches |
| One replica | `file` | Yes (the directory is the truth) | n/a | Pointing mockulus at a WireMock project a team already has |

### One replica, memory store

This is what `docker run` and `helm install mockulus deploy/chart` give you with
no configuration at all. No Couchbase, no persistence, no propagation delay,
nothing to operate. A WireMock client pointed at the Service works immediately.

It is the honest first step of a migration: run your existing suite against it,
find out which stubs come back 422, fix or defer those, and only then decide
whether you want the clustered shape. It is also the right answer for a CI
runner that wants genuine isolation — one deployment is one global namespace for
stubs, scenarios and the journal (SPEC §1), so runners that share an instance
must keep their stubs distinguishable and must never call `POST /__admin/reset`
or `DELETE /__admin/mappings`. A runner that cannot promise that gets its own
single-replica instance instead.

The cost is the obvious one: the pod restarts and the stubs are gone.

### N replicas over Couchbase

This is the shape mockulus exists for. Stubs are persisted in Couchbase and
served from an in-memory snapshot on every pod, so any replica answers any mock
request and any admin call. Replica count is free — Couchbase load does not grow
with it beyond one cheap epoch read per pod per `sync_interval`, and scenario
and journal load track feature use rather than pod count.

A stub registered through the Service is spliced into the snapshot of the pod
that handled the write immediately, and reaches the others within
`sync_interval`. Section 7 below has the details and the bound.

### One replica, file store

`store: file` with `file.root` pointing at a directory containing `mappings/`
and `__files/` reads a WireMock project directory directly. The directory is the
source of truth, which means the driver is **read-only**: every admin write
answers 503, the same answer any store that cannot take a write gives.

```console
$ curl -s -w '\n%{http_code}\n' -X POST http://127.0.0.1:19317/__admin/mappings \
    -H 'Content-Type: application/json' \
    -d '{"request":{"method":"GET","url":"/x"},"response":{"status":200}}'
{"errors":[{"code":1020,"title":"Store unavailable","detail":"the stub store is unavailable; the admin write was not applied"}]}
503
```

Edits to the tree converge through the same reload path as any other change:
the driver fingerprints paths, sizes and mtimes, so a saved file is picked up
within `sync_interval`. Scenario state is the one thing it keeps in process,
because the serve path has to be able to advance it.

The file store does not implement the journal. `journal_enabled: true` with it
is refused at startup rather than silently doing nothing:

```console
$ MOCKULUS_STORE=file MOCKULUS_FILE_ROOT=/tmp/wm MOCKULUS_JOURNAL_ENABLED=true mockulus
mockulus: journal_enabled is set but the file store cannot record a journal
```

### The combination that is not a deployment

Several replicas over the `memory` or `file` driver looks like it works. Pods
start, probes pass, the Service has endpoints. But each pod keeps its stubs
inside its own process, so a stub registered through the Service lands on
exactly one pod and 404s on the others at a rate the load balancer picks.

The chart refuses to render it:

```console
$ helm template mockulus deploy/chart --set replicaCount=3
Error: execution error at (mockulus/templates/deployment.yaml:2:4):

  mockulus: 3 replicas with the "memory" store is not a working deployment.

  The memory driver keeps stubs inside a single process, so a stub registered
  through the Service would be served by one pod and 404 on the others.

  Choose one:

    * one replica — the WireMock drop-in mode, and the migration on-ramp:
        --set replicaCount=1

    * a shared store, which is what makes replicas interchangeable:
        --set couchbase.connstr=couchbase://cb.mockulus.svc \
        --set couchbase.existingSecret=mockulus-couchbase

  See SPEC §15.4 and deploy/chart/README.md.
```

The guard looks at the *highest* replica count the release can reach, so an HPA
counts too — `--set autoscaling.enabled=true` over the memory store is refused
on `autoscaling.maxReplicas`, not on `replicaCount`:

```console
$ helm template mockulus deploy/chart --set autoscaling.enabled=true 2>&1 | head -3
Error: execution error at (mockulus/templates/deployment.yaml:2:4):

  mockulus: 10 replicas with the "memory" store is not a working deployment.
```

Adding a shared store makes the same command render:

```console
$ helm template mockulus deploy/chart --set replicaCount=3 \
    --set couchbase.connstr=couchbase://cb.mockulus.svc \
    --set couchbase.existingSecret=mockulus-couchbase \
    | grep -E '^(kind|  replicas)'
kind: PodDisruptionBudget
kind: Service
kind: Deployment
  replicas: 3
```

---

## 2. Installing

### Helm

The chart lives at [`deploy/chart`](../deploy/chart). The zero-config install is
one replica over the memory store:

```sh
helm install mockulus deploy/chart
```

Persistence and clustering come from a connection string and a Secret:

```sh
helm install mockulus deploy/chart \
  --set replicaCount=3 \
  --set couchbase.connstr=couchbase://cb.mockulus.svc \
  --set couchbase.existingSecret=mockulus-couchbase
```

`couchbase.existingSecret` must hold `username` and `password` keys (rename them
with `couchbase.usernameKey` / `couchbase.passwordKey`). Passing
`couchbase.username` and `couchbase.password` instead makes the chart create the
Secret for you, which puts both in the release history — fine for a throwaway
cluster, not for anything else. The chart refuses a connection string with
neither form supplied.

Every `config.*` value maps to the `MOCKULUS_*` environment variable of
[SPEC §13](../SPEC.md#13-configuration-reference), which
[configuration.md](configuration.md) walks through. Anything the chart does not
surface as a named value goes through `config.extraEnv`:

```yaml
config:
  extraEnv:
    MOCKULUS_SCENARIO_KV_TIMEOUT: 400ms
    MOCKULUS_COUCHBASE_KV_TIMEOUT: 4s
```

Values worth deciding deliberately rather than inheriting:

| Value | Default | Why you might move it |
|---|---|---|
| `config.journalEnabled` | `false` | Suites that call `verify()` need it. Load tests must not have it. |
| `config.diagnosticsOnUnmatched` | `false` | Near-miss detail in 404 bodies. A debugging aid that costs CPU on every unmatched request. |
| `config.syncInterval` | `1s` | The cross-replica propagation bound. Floor is `100ms`. |
| `config.resyncInterval` | `5m` | Unconditional full reload; also the window in which a naturally expired ephemeral stub can still match. |
| `config.ephemeralStubTtl` | `24h` | How long a `persistent:false` stub survives. `0` disables the TTL. |
| `resources.limits` | `2` CPU / `512Mi` | The [SPEC §16](../SPEC.md#16-performance-engineering) SLOs are measured on exactly this. Shrink it and the SLOs do not apply. |
| `probes.startup` | `5s × 12` | 60 s to complete the first load. Raise `failureThreshold` for very large stub sets. |
| `terminationGracePeriodSeconds` | `30` | Must exceed `preStopSleepSeconds` + `shutdown_drain` + `shutdown_timeout`. See section 5. |

### Scaling

Mock traffic is CPU-bound, so CPU-target autoscaling is the right default:

```console
$ helm template mockulus deploy/chart \
    --set autoscaling.enabled=true --set autoscaling.maxReplicas=10 \
    --set couchbase.connstr=couchbase://cb.mockulus.svc \
    --set couchbase.existingSecret=mockulus-couchbase \
    | sed -n '/kind: HorizontalPodAutoscaler/,/^---/p' | tail -13
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: mockulus
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

To scale on latency
or throughput instead, run Prometheus Adapter over
`mockulus_http_request_duration_seconds` and pass your own rule through
`autoscaling.metrics`; the shape is in the comments of `values.yaml`.

Replica count is otherwise free — any pod serves any request and any admin call
— and Couchbase load does not grow with it beyond the one epoch read per pod per
`sync_interval`. The constraint is the one from section 1: **autoscaling requires
a shared store**, and the chart's guard reads `autoscaling.maxReplicas`, so
enabling an HPA over the memory store is refused at render time.

### kustomize

`deploy/manifests` is a plain kustomize base for teams that would rather not run
Helm:

```console
$ kubectl kustomize deploy/manifests | grep -E '^kind:|  replicas:'
kind: Service
kind: Deployment
  replicas: 2
kind: PodDisruptionBudget
```

Note the `replicas: 2` with no store configured. The base has no render-time
guard — that logic lives in the chart's templates — so **the base as shipped is
the combination section 1 says is not a deployment**. Before applying it, either
patch the replica count to 1 or patch in `MOCKULUS_COUCHBASE_CONNSTR` and a
credential Secret through an overlay. The chart is the supported path precisely
because it can refuse.

### The hardened preset

The admin API is open by default. That assumes the deployment is reachable only
from inside the cluster, which is the posture [SPEC §17](../SPEC.md#17-security)
expects. When the mock port is reachable more broadly, that assumption stops
holding, and `values-hardened.yaml` is the preset that fixes it: the admin API
comes off the mock port, a bearer token is required, and a NetworkPolicy keeps
the ops port inside the namespace and the monitoring namespace.

The preset refuses to render without a token:

```console
$ helm template mockulus deploy/chart -f deploy/chart/values-hardened.yaml
Error: execution error at (mockulus/templates/deployment.yaml:2:4):

  mockulus: adminAuth.required is set and no token was supplied.

  The hardened preset asks for one because the rest of it — the admin API off
  the mock port, a NetworkPolicy around the ops port — narrows who can reach
  the admin API without ever requiring a credential from them. Rendering
  without the token would produce a release that reads as locked down and has
  an open admin API, which is worse than no preset at all.

    --set adminAuth.existingSecret=mockulus-admin-token

  Prefer an existing Secret over adminAuth.token: a token passed as a value is
  a token in the release history.
```

This is the whole point of the preset being a preset. A hardened values file
that renders happily with `adminAuth.token` left empty produces a release that
reads as locked down in review and has an open admin API in the cluster.
Failing at render time is the same fail-loud contract the admin API applies to
unsupported stubs.

With a token it renders:

```console
$ helm template mockulus deploy/chart -f deploy/chart/values-hardened.yaml \
    --set adminAuth.existingSecret=mockulus-admin-token \
    | grep -nE 'kind: NetworkPolicy|MOCKULUS_ADMIN_ON_MOCK_PORT|MOCKULUS_ADMIN_AUTH_TOKEN|name: mockulus-admin-token'
4:kind: NetworkPolicy
134:            - name: MOCKULUS_ADMIN_ON_MOCK_PORT
156:            - name: MOCKULUS_ADMIN_AUTH_TOKEN
159:                  name: mockulus-admin-token
```

Supply the token through `adminAuth.existingSecret` rather than
`adminAuth.token`, so it never lands in a values file or a release history.

---

## 3. Couchbase setup

### What you create, and what mockulus creates

mockulus asks for as few cluster rights as it can. The split is:

| Object | Who creates it | Notes |
|---|---|---|
| The bucket (default `mockulus`) | **You** | Creating a bucket needs cluster-manager rights mockulus deliberately does not ask for. |
| The scope (default `_default`) | mockulus, at boot | Only when it is not `_default`. |
| Collections `mappings`, `files`, `scenarios`, `journal`, `meta` | mockulus, at boot | |
| GSI `ix_journal_ts` on `journal(ts)` | mockulus, at boot | Backs journal time-window queries. |
| Primary indexes on `mappings` and `files` | mockulus, at boot | Only on servers without KV range scan — see below. |
| The `schema` document in `meta` | mockulus, at boot | A plain KV write, not DDL. Refuses to start against documents written by a newer build. |

The scope, the collections and the two index rows are gated by
`couchbase.manage_bucket` (`manageBucket` in the chart), which defaults to
`true`. That is what keeps the zero-config promise: point mockulus at an empty
bucket and it is enough. The `schema` marker is written either way, because it
is an ordinary document rather than schema management.

The application user needs data read/write on the bucket, plus enough rights to
create collections and run the `CREATE INDEX` above. Where your policy will not
grant that, set `couchbase.manage_bucket: false` and apply the DDL out of band
before the first rollout — the collections and `ix_journal_ts` must exist, or
the pods will fail to reach a store they cannot bootstrap.

`scripts/kind-couchbase.sh` in this repository is a worked example of the split.
It creates the bucket with `couchbase-cli bucket-create` and leaves the scope,
the collections and the index to `manage_bucket` at boot, precisely because that
is the path worth testing.

### Deployment-per-team

There is no in-app multi-tenancy (SPEC D12). Teams that need isolation get their
own release, and can share one bucket by taking different `couchbase.scope`
values — the scope is the isolation boundary, and mockulus creates it for you
when it is not `_default`.

### Range scan and the N1QL fallback

Bulk loads use KV range scan on Couchbase 7.6 and newer. mockulus probes for it
by trying one rather than reading a version string, and falls back to N1QL when
it is unavailable — logging the fact, and creating the primary indexes that
fallback needs:

```
level=INFO msg="KV range scan is unavailable; bulk loads will use N1QL"
  hint="range scan needs Couchbase 7.6 or newer"
```

If you see that line, either you are on an older server or the probe could not
complete. It is not an error, but the N1QL path is the slower one and it is
carrying primary indexes you would otherwise not have.

### Durability

Admin writes default to `durability: none`, which is the fast, test-oriented
setting. Teams that treat their mocks as long-lived environment configuration
rather than per-run scaffolding should set `couchbase.durability: majority` and
accept the added write latency — the S10 SLO (p99 create < 150 ms under a CI
write storm) is stated at `none`.

### Timeouts

`couchbase.kv_timeout` (2.5 s) and `couchbase.query_timeout` (10 s) govern the
admin and reload paths. `scenario_kv_timeout` (250 ms) is separate and much
tighter, because it is the only Couchbase call on the request path: a sick node
must not stall a mock response beyond that budget. If you are seeing
`scenarioUnavailable` 500s under an otherwise healthy cluster, that is the knob,
and raising it trades correctness-fast-failure for latency.

---

## 4. Probes and what readiness means

| Probe | Path | Port | Meaning |
|---|---|---|---|
| Liveness | `/healthz` | ops (9090) | The process is up. **Never** consults the store. |
| Readiness | `/readyz` | ops (9090) | A valid snapshot is loaded and the listeners are bound. |
| Startup | `/readyz` | ops (9090) | Same check, with a budget for the first load. |

```console
$ curl -si http://127.0.0.1:19311/healthz | head -1
HTTP/1.1 200 OK
$ curl -s -w ' | %{http_code}\n' http://127.0.0.1:19311/readyz
ready | 200
```

Liveness deliberately does not depend on Couchbase. A store outage must not
restart pods: the snapshot they are holding is still servable, and restarting
them throws it away and leaves a fleet of not-ready pods behind an empty
Service. If you remember one thing from this document, make it that one —
restarting the pods is how a survivable Couchbase outage becomes a total
mockulus outage.

Readiness means "this pod can serve mock traffic". It goes 200 once the initial
load has completed, and it **stays 200 through a Couchbase outage**, because the
loaded snapshot is still good. It goes 503 only before the first load completes
and after SIGTERM.

The chart's startup probe is `periodSeconds: 5`, `failureThreshold: 12` — 60 s
for the first load, against an SLO of under 5 s for 10k stubs from Couchbase
(SPEC §16.1 S6). Raise `probes.startup.failureThreshold` for very large stub
sets rather than raising the liveness threshold; that is what the startup probe
is for.

### Boot with Couchbase unreachable

The pod stays alive and not-ready and retries forever, with backoff. It does not
crash-loop, and it does not bind the mock port at all — there is nothing to
serve yet, so there is no listener to route to:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:19319/healthz
200
$ curl -s -w ' | %{http_code}\n' http://127.0.0.1:19319/readyz
not ready | 503
$ curl -s -w '\n%{http_code}\n' http://127.0.0.1:19319/__admin/mappings
{"errors":[{"code":1020,"title":"Store unavailable","detail":"the store is not connected yet; this pod is still starting"}]}
503
$ curl --max-time 2 http://127.0.0.1:19318/anything
curl: (7) Failed to connect to 127.0.0.1 port 19318 after 2 ms: Couldn't connect to server
```

The log tells you what it is waiting for:

```
level=WARN msg="couchbase is unreachable; retrying" attempt=1 retry_in=500ms
  error="bucket mockulus not ready: unambiguous timeout | {\"operation_id\":\"WaitUntilReady\",
  \"time_observed\":10000980,\"retry_reasons\":[\"NOT_READY\",\"CONNECTION_ERROR\"],\"retry_attempts\":22}"
```

Backoff starts at 500 ms and doubles to a 15 s ceiling. This is the K8s-idiomatic
shape: Kubernetes does not route to a pod that never reports ready, and the pod
is still there to come up the moment the store does.

### The container HEALTHCHECK

The image carries `HEALTHCHECK` running `mockulus -healthcheck`, which probes
`/healthz` on the *configured* admin port — it reads the same configuration the
server does, so it checks the port this deployment actually bound rather than
the default:

```console
$ MOCKULUS_ADMIN_PORT=19311 mockulus -healthcheck; echo "exit=$?"
exit=0
$ MOCKULUS_ADMIN_PORT=19999 mockulus -healthcheck; echo "exit=$?"
mockulus: healthcheck: Get "http://127.0.0.1:19999/healthz": dial tcp 127.0.0.1:19999: connect: connection refused
exit=1
```

It exists for teams running the image directly with `docker run` — the
distroless base has no shell and no curl, so the binary is the only thing in the
image that can make a request. Kubernetes uses the probes above instead. The
check deliberately does not aim at the mock port, because an unmatched mock
request is a 404 by design and a check pointed there would report a healthy pod
as broken the moment a suite reset its stubs.

### Configuration errors

Configuration is validated before anything binds, and every problem is reported
at once so a misconfigured deployment does not need one restart per mistake:

```console
$ MOCKULUS_SYNC_INTERVAL=50ms mockulus; echo "exit=$?"
mockulus: invalid configuration:
  - sync_interval: 50ms is below the 100ms minimum
exit=1
```

TLS key pairs are loaded during that validation, so an unusable certificate
exits 1 rather than failing the first handshake on a pod Kubernetes has already
routed traffic to.

---

## 5. Rolling restarts and the drain

The shutdown sequence is [SPEC §4.5](../SPEC.md#45-shutdown-sequence):

```
SIGTERM → /readyz flips to 503
        → wait shutdown_drain (default 5 s)
        → http.Server.Shutdown on both listeners (deadline shutdown_timeout, 15 s)
        → flush the journal batch
        → close the store
        → exit 0
```

Readiness drops **first** and traffic keeps being served for the whole drain
window. That is the entire trick: the window is there so that endpoint removal
can propagate through every kube-proxy and every in-cluster client's connection
pool before the listener actually closes.

Verified against a running instance — SIGTERM sent at t+0, a stub registered
beforehand, polling both ports once a second:

```console
$ kill -TERM $(cat drain.pid)
$ for i in 1 2 3 4 5 6; do
    R=$(curl -s -o /dev/null -w '%{http_code}' --max-time 1 http://127.0.0.1:19313/readyz || echo refused)
    M=$(curl -s --max-time 1 http://127.0.0.1:19312/drain || echo refused)
    echo "t+${i}s readyz=$R mock=$M"; sleep 1
  done
t+1s readyz=503 mock=served
t+2s readyz=503 mock=served
t+3s readyz=503 mock=served
t+4s readyz=503 mock=served
t+5s readyz=503 mock=served
t+6s readyz=000refused mock=refused
```

```
level=INFO msg="shutting down"
level=INFO msg=draining duration=5s
level=INFO msg=stopped
```

The chart pairs this with a `preStop` sleep of 3 seconds, which covers the
*other* end of the same race: the window between the kubelet starting
termination and the endpoint deletion reaching every proxy. Between them there
is no moment where a pod is both routed to and refusing connections, which is
why a rolling restart serves no 5xx. CI asserts exactly that —
`scripts/kind-rollout.sh` drives traffic through the Service from inside the
cluster for the whole of a `kubectl rollout restart` and fails on a single
non-200.

**Watch the grace period arithmetic.** The default budget is:

```
preStopSleepSeconds (3) + shutdown_drain (5) + shutdown_timeout (15) = 23 s
terminationGracePeriodSeconds = 30 s
```

If you raise `shutdown_drain` — a large cluster with slow endpoint propagation
is a real reason to — raise `terminationGracePeriodSeconds` with it. Once the
grace period expires the kubelet sends SIGKILL, the journal batch is not flushed
and in-flight responses are cut off mid-write. The two numbers have to move
together.

`preStop.sleep` is the native Kubernetes sleep action, which is enabled by
default from Kubernetes 1.30. On older clusters replace it with an `exec` hook —
but note that the distroless image has no shell, so that means changing the base
image as well as the hook.

---

## 6. Degraded modes

This is the section a pager holder needs. The contract is
[SPEC §4.6](../SPEC.md#46-degraded-modes-explicit-contract); what follows is
what it means at 3am.

### Couchbase goes away while the pods are running

| Still works | Starts failing |
|---|---|
| All mock traffic against plain stubs, served from the last snapshot | Admin **writes** → 503, error code 1020 `storeUnavailable` |
| `/healthz` (200), `/readyz` (200) | Requests hitting a stub that is *in a scenario* → 500, code 1021 `scenarioUnavailable` |
| Templating, delays, faults, response files already resolved into the snapshot | Journal entries — dropped and counted |
| `/metrics`, `/debug/pprof` | Snapshot reloads — the previous snapshot keeps serving |

Read that table twice. **Mock traffic keeps working.** A Couchbase outage is not
a mockulus outage for anything that was already registered and is not part of a
state machine. Do not restart the pods: readiness is 200 on purpose, and a
restart is the one action that turns a survivable outage into a total one,
because a restarted pod has no snapshot and cannot load one.

Scenario stubs are the deliberate exception. Scenario state lives in Couchbase
and is read on the request path, so a state read that fails is answered 500
rather than guessed at — serving the wrong side of a state machine because the
store hiccuped is worse than saying so. Plain stubs are untouched, which keeps
the blast radius to the deployments actually using scenarios.

Recovery needs no intervention. The Couchbase SDK reconnects underneath, the
next poll tick's reload succeeds, and admin writes start being accepted again.
Nothing was buffered while the store was away, so there is nothing to reconcile
and no window in which a pod serves stubs the cluster does not have.

### Couchbase is down when a pod boots

Covered in section 4: alive, not ready, retrying forever, mock port not bound.
Kubernetes does not route to it. When the store appears the pod loads and joins.

### A reload fails while the pods are running

A `LoadAll` error does **not** clear the snapshot. The previous snapshot keeps
serving, the error is logged, `mockulus_snapshot_reload_failures_total` is
incremented, and the poller retries on the next tick. A cluster-wide store
problem therefore shows up as a rising failure counter and a stub set that has
stopped moving, not as a fleet serving 404s.

### An individual stub does not compile

A single bad document never aborts a build — otherwise one bad stub would freeze
propagation across the whole cluster, and a rolling upgrade would wedge every
old-version pod for the length of the window. The document is excluded from the
snapshot, logged, and counted:

```
level=WARN msg="stub quarantined: mapping does not compile" id=7c6df22a-0230-5779-9135-1b73d0b71683 problems=1
```

```console
$ curl -s http://127.0.0.1:19317/metrics | grep quarantined
mockulus_snapshot_quarantined_total{reason="compile"} 2
mockulus_snapshot_stubs 2
```

That transcript is from a `file`-store instance pointed at a WireMock project
containing a stub with `equalToXml` — an unsupported matcher (see
[ROADMAP.md](../ROADMAP.md) §1.1). Two other stubs in the same directory loaded
and serve normally.

Note the counter semantics: the quarantine counter is incremented **per bad
document per snapshot build**, so a document that stays broken makes it climb
every reload. Alert on its rate, not on its total (section 8).

A related case has different handling: a stub whose `bodyFileName` names a file
that does not exist still enters the snapshot, and serving it returns 500 with
"body file not found" (code 1022). Registering a stub before uploading its file
is legal — the later file `PUT` bumps the epoch and the next rebuild resolves
the body.

### The journal queue fills up

Entries are dropped and `mockulus_journal_dropped_total` is incremented. The hot
path is never blocked or slowed; that is the whole design of the journal being
opt-in and asynchronous. A non-zero drop rate means your `verify()` calls are
now under-reporting, which is a correctness problem for the suite and not an
availability problem for the deployment. Either raise `journal_buffer` /
`journal_buffer_bytes` and `journal_flush_workers`, or accept that the traffic
level is past what the journal is for. Load tests keep it off entirely.

### `start_without_store` — leave it off

`start_without_store` (default `false`) is an escape hatch that makes a pod
become ready even when the store is unreachable at boot. **In the current build
it does more than that, and the difference matters.** When the store cannot be
opened, the pod substitutes a private in-process store and keeps it for the rest
of its life:

```console
$ curl -s -w ' | %{http_code}\n' http://127.0.0.1:19321/readyz
ready | 200
$ curl -s -w '\n%{http_code}\n' -X POST http://127.0.0.1:19321/__admin/mappings \
    -H 'Content-Type: application/json' \
    -d '{"request":{"method":"GET","url":"/x"},"response":{"status":200}}'
{"id":"3eccdbf9-f0cf-44b7-8790-3444b0986883","request":{"method":"GET","url":"/x"},"response":{"status":200},"uuid":"3eccdbf9-f0cf-44b7-8790-3444b0986883"}
201
$ curl -s http://127.0.0.1:19321/__admin/health
{"epoch":1,"message":"mockulus is ok","status":"healthy","store":{"driver":"couchbase"},"stubs":1,"timestamp":"2026-07-29T07:15:24.889171Z","uptimeInSeconds":15,"version":"dev"}
```

The write was accepted, it is held only in that pod, the pod does not reconnect
to Couchbase when it returns, and `/__admin/health` still reports the store
driver as `couchbase`. In a multi-replica deployment that is a silently diverged
replica serving stubs no other pod has — the hardest kind of incident to see
from the outside.

The safe posture is the default. Leave `start_without_store: false` and let the
pod stay not-ready until its store is there; Kubernetes handles that correctly
without help.

---

## 7. Propagation between replicas

Every pod polls a single counter document in Couchbase — one KV get per
`sync_interval` (default 1 s), which is nothing at any replica count. When the
epoch differs from the snapshot's, the pod does a full reload: read everything,
compile, swap. Convergence is level-triggered, so there is no missed-event class
of bug — any change to the epoch reconciles the pod to the current state of the
store, whatever it missed.

Two paths, two latencies:

- **The pod that handled the admin write** splices the compiled stub into its
  own snapshot immediately, without a recompile. A single-pod test flow —
  register, call, verify — sees **zero** staleness.
- **Every other pod** sees it on its next poll. The bound is `sync_interval` plus
  one warm reload plus a small margin: ≤ 1.5 s at defaults with 10k stubs (SPEC
  §16.1 S9). This is deviation #11 in SPEC §5.5.

Reloads are coalesced per pod (single-flight plus a dirty flag), so reload
frequency is capped at roughly one per `sync_interval` per pod **regardless of
how fast the cluster as a whole is writing**. A hundred CI runners registering
stubs at once does not turn into a hundred rebuilds.

`resync_interval` (5 m) forces an unconditional reload even without an epoch
change. It sweeps documents whose TTL has expired and self-heals any signal a
pod somehow missed.

That sweep is where the one genuinely surprising propagation rule comes from.
Ephemeral stubs (`persistent:false`, the default) carry a 24 h TTL, and a TTL
expiry does **not** bump the epoch — nothing wrote anything. So a naturally
expired stub can keep matching on pods that already hold it for up to
`resync_interval`, five minutes by default. Explicit deletes and resets bump the
epoch and propagate within `sync_interval` like anything else. This is deviation
#17, and it is the reason a suite that depends on a stub being gone should
delete it rather than wait for it to expire.

To check convergence, compare `epoch` across pods:

```console
$ curl -s http://127.0.0.1:19311/__admin/health
{"epoch":1,"message":"mockulus is ok","status":"healthy","store":{"driver":"memory"},"stubs":1,"timestamp":"2026-07-29T07:11:49.721147Z","uptimeInSeconds":42,"version":"dev"}
```

The same two numbers are on the ops port as `mockulus_snapshot_epoch` and
`mockulus_snapshot_stubs`, which is the form to use for an alert — a pod whose
epoch lags the fleet's maximum for more than a few multiples of `sync_interval`
is a pod that has stopped reloading.

---

## 8. What to alert on

Metrics are Prometheus, on `/metrics` on the ops port, and low-cardinality by
design — there are no per-stub labels, so a 10k-stub deployment does not mint
10k series. The full list is [SPEC §14.1](../SPEC.md#141-metrics-prometheus-metrics-on-admin-port).
These are the ones worth a rule.

| Signal | Metric | Why |
|---|---|---|
| **Store is failing** | `rate(mockulus_store_errors_total[5m]) > 0` | The `op` label names what failed (`load_all`, `put_stub`, `bump_epoch`, `next_seq`, `scenario_read`, …). `scenario_read` is the one that is visible to mock clients as 500s. |
| **The fleet has stopped converging** | `rate(mockulus_snapshot_reload_failures_total[5m]) > 0` | Reloads are being abandoned; pods are serving a snapshot that is getting older. Page if it persists past a couple of `sync_interval`s. |
| **Stubs are being silently excluded** | `rate(mockulus_snapshot_quarantined_total[15m]) > 0` | Someone registered or edited a document that does not compile on this build. Ticket, not a page — but it explains a 404 nobody can account for. Rate, not total: the counter re-increments on every build while the document is there. |
| **Verification is under-reporting** | `rate(mockulus_journal_dropped_total[5m]) > 0` | Only meaningful with `journal_enabled: true`. Suites will get wrong `verify()` answers. |
| **Someone is guessing the admin token** | `rate(mockulus_admin_requests_total{code="401"}[5m]) > 0` | Refusals are counted deliberately, so a deployment being probed does not look idle. |
| **Latency SLO** | `histogram_quantile(0.99, sum by (le) (rate(mockulus_http_request_duration_seconds_bucket[5m])))` | The SLO targets in SPEC §16.1 (p99 < 2 ms for exact-URL matches at 20 k RPS) hold on the reference rig — 1 pod, 2 vCPU, 512 MiB. Set your own threshold against your own measurement. |
| **Unmatched-request rate** | `mockulus_http_requests_total{matched="false"}` vs `{matched="true"}` | A sudden jump usually means a reset destroyed someone's stubs, or a rollout brought up pods that have not converged. Very good early warning; noisy as a page. |
| **Epoch skew** | `max(mockulus_snapshot_epoch) - min(mockulus_snapshot_epoch)` | Non-zero for longer than a few seconds means a pod is not reloading. |
| **Reload cost** | `mockulus_snapshot_reload_duration_seconds` | The SLO is < 2 s cold and < 500 ms warm at 10k stubs. A climb here is the leading indicator of the stub set outgrowing the pod. |
| **Regex is timing out** | `rate(mockulus_regex_timeouts_total[5m]) > 0` | A pathological pattern is failing closed as a non-match. The offending pattern is in the WARN log. |
| **Template errors** | `rate(mockulus_template_render_errors_total[5m]) > 0` | Render errors go into the response body, so clients are receiving error text where they expect a payload. |

`mockulus_build_info{version,go_version}` is the series to join on for
version-aware rules and for confirming a rollout actually replaced everything.

Scraping is configured by `serviceMonitor.enabled=true` in the chart, which
emits a ServiceMonitor against the `admin` port at `/metrics` every 30 s.

Logs are `log/slog` JSON on stdout. The hot path is silent unless
`log.requests` is on, and it is sampled when it is (`log.request_sample_n`,
every 100th request by default) — leave it off under load. Admin mutations are
logged at info. Stub bodies and headers are never logged at any level, because
teams put real credentials in their mocks.

---

## 9. Security posture

The full statement is [SPEC §17](../SPEC.md#17-security) and
[SECURITY.md](../SECURITY.md). Operationally:

**The admin API is open by default.** That is a deliberate choice for the
in-cluster case, where the expected posture is a NetworkPolicy plus
`admin_on_mock_port: false` when the mock port is exposed beyond the namespace.
Anything less contained gets the hardened preset from section 2.

**`admin_auth_token` guards the whole `/__admin` mux**, not individual routes,
so a route added in a later version is protected by existing. It also guards
`/debug/pprof/**`, because a heap profile is a copy of every stub body the
process is holding — which is exactly what the token exists to protect.
`/healthz`, `/readyz` and `/metrics` stay open whatever the token setting: the
kubelet and Prometheus cannot present one, and none of the three carries stub
content.

```console
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:19315/__admin/mappings
401
$ curl -s -H 'Authorization: Token wrong' http://127.0.0.1:19315/__admin/mappings
{"errors":[{"code":10,"title":"Malformed request","detail":"admin API requires a valid Authorization token"}]}
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Token s3cret' http://127.0.0.1:19315/__admin/mappings
200
$ for p in /healthz /metrics; do
    printf "%-10s %s\n" "$p" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:19315$p)"
  done
/healthz   200
/metrics   200
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:19315/debug/pprof/
401
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Token s3cret' http://127.0.0.1:19315/debug/pprof/
200
```

Refusals are counted, so a deployment whose token is being guessed does not look
idle:

```console
$ curl -s http://127.0.0.1:19315/metrics | grep 'code="401"'
mockulus_admin_requests_total{code="401",endpoint_group="mappings"} 2
```

The comparison is constant-time. Rotating the token means rolling the Deployment,
since it is read from the environment at startup.

**`POST /__admin/shutdown` is disabled by default** and answers the
unsupported-endpoint 404 rather than existing and refusing — an unauthenticated
caller who can stop a replica is a denial of service with an HTTP interface, and
Kubernetes owns the pod lifecycle here anyway. `admin_shutdown_enabled: true`
turns it on where a test harness genuinely needs it.

**The NetworkPolicy** in the chart (`networkPolicy.enabled`) leaves the mock port
open to everything and restricts the ops port to the release's own namespace and
a monitoring namespace matched by the `kubernetes.io/metadata.name` label. It
declares `policyTypes: [Ingress]` only, so egress — including to Couchbase — is
unrestricted; add your own egress policy if your cluster expects one.

**The container** runs as nonroot (uid 65532), read-only root filesystem, all
capabilities dropped, `RuntimeDefault` seccomp, on a distroless base with no
shell. The chart sets all of this by default, not only in the hardened preset.

**Treat the bucket as sensitive.** Stub bodies routinely contain the credentials
and payloads of the systems being mocked, and they are stored verbatim. The
journal, when enabled, stores request bodies too — bounded by `journal_ttl`
(30 m) and capped per entry at `journal_max_body` (64 KiB), which is the
retention story, but for the window it exists it is real traffic on disk.

**Templates are sandboxed by construction**: an allowlist of helpers, with no
file, environment, network or system access. A stub cannot read a secret out of
the pod because there is no helper that reads anything.

---

## 10. Quick reference

Ports and paths:

| | Mock port (8080) | Ops port (9090) |
|---|---|---|
| Stub traffic | yes | no |
| `/__admin/**` | yes, unless `admin_on_mock_port: false` | always |
| `/healthz`, `/readyz` | no (404) | yes, never authenticated |
| `/metrics` | no (404) | yes, never authenticated |
| `/debug/pprof/**` | no (404) | yes, behind the admin token when one is set |

Error codes you will see in an incident:

| Code | HTTP | Meaning |
|---|---|---|
| 1020 | 503 | Store unavailable — the admin write was not applied |
| 1021 | 500 | Scenario state could not be read |
| 1022 | 500 | A stub's `bodyFileName` names a file that does not exist |
| 1010 | 500 | The journal is disabled — turn on `journal_enabled` for suites that `verify()` |
| 1030 | 413 | Request body exceeded `max_body_bytes` (10 MiB by default) |
| 1001 | 404 | Endpoint outside the supported matrix — see ROADMAP.md |
| 1000 | 422 | Unsupported stub feature — the `pointer` names the field |

The complete catalog is [SPEC Appendix B](../SPEC.md#appendix-b--error-catalog);
the full configuration table, with every key, default and description, is
[SPEC §13](../SPEC.md#13-configuration-reference).
