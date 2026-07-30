# Configuration

Mockulus starts with no configuration at all. `./bin/mockulus` binds `:8080` for
mock traffic and `:9090` for admin and operations, keeps stubs in memory, leaves
the journal off and templating on WireMock's own terms. That is a working
single-node WireMock replacement, and it is the shape most people should try
first.

Everything below is what you change when one replica in one process is no longer
the deployment you have.

## Where the numbers in this document come from

The authoritative list of keys, their defaults and their one-line descriptions is
[SPEC §13](../SPEC.md#13-configuration-reference). That table is **generated**
from the Go struct that the binary actually binds — `make config-docs`
regenerates it and CI fails the build when the committed table and the code
disagree. There is no second table in this repository that can rot, and this
document deliberately does not become one.

So: for "what is the exact default of `journal_batch_size`", read §13. For "what
happens if I change it, and why would I", read this. Where a key needs a value
spelled out here to make a paragraph mean anything, it is spelled out — but §13
is the source, and if the two ever differ, §13 is right and this file has a bug.

[`compatibility.md`](compatibility.md#configuration-keys) carries the same table
again with, for each key, the E2E behavior identifier and the number of cases in
the gate that exercise it.

This is a reference, not a tutorial. If you have not started the server yet,
[`getting-started.md`](getting-started.md) is the shorter road; if you are
deciding what to deploy rather than which key to set,
[`operations.md`](operations.md) works from topologies down.

---

## The three sources, and which one wins

A setting can come from three places. In decreasing order of precedence:

1. **An environment variable** — `MOCKULUS_JOURNAL_TTL=90s`
2. **A YAML file** — named by the `--config` flag, or by `MOCKULUS_CONFIG`
3. **The built-in default**

Env beats file beats default, per key. There is no partial-merge subtlety: each
key is resolved on its own, so a file that sets ten things and an environment
that overrides one leaves the other nine as the file wrote them.

`--config` and `MOCKULUS_CONFIG` name the same thing, and the flag wins. The
environment variable is there because a Kubernetes container spec is a more
natural place to put a path than an args list; the flag is there because typing
one on a laptop is faster.

```sh
MOCKULUS_CONFIG=/etc/mockulus/a.yaml mockulus --config ./b.yaml   # b.yaml is read
```

### The naming rule

An environment variable name is `MOCKULUS_` followed by the YAML path in upper
snake case, with dots becoming underscores.

| YAML key | Environment variable |
|---|---|
| `port` | `MOCKULUS_PORT` |
| `sync_interval` | `MOCKULUS_SYNC_INTERVAL` |
| `couchbase.connstr` | `MOCKULUS_COUCHBASE_CONNSTR` |
| `couchbase.kv_timeout` | `MOCKULUS_COUCHBASE_KV_TIMEOUT` |
| `log.request_sample_n` | `MOCKULUS_LOG_REQUEST_SAMPLE_N` |

The rule is mechanical and total — it is applied by the same reflection pass
that generates §13, so every key in §13 has an environment variable and its name
is derivable without looking anything up.

### Value syntax

**Durations** are Go duration strings: `1s`, `200ms`, `2500ms`, `30m`, `24h`.
Not seconds-as-a-number. A bare integer is rejected, on purpose — `sync_interval:
5` is ambiguous enough to be worth a startup failure:

```console
$ MOCKULUS_SYNC_INTERVAL=5 mockulus
mockulus: invalid environment configuration:
  - MOCKULUS_SYNC_INTERVAL: invalid duration "5" (want Go syntax, e.g. 1s, 200ms)
```

**Sizes** are bytes, optionally suffixed `KiB`, `MiB` or `GiB` — binary units
only, because that is what a memory limit is measured in:

```console
$ MOCKULUS_MAX_BODY_BYTES=10MB mockulus
mockulus: invalid environment configuration:
  - MOCKULUS_MAX_BODY_BYTES: invalid size "10MB"
```

`10MiB` and `10485760` are both accepted and identical.

**Booleans** are `true` / `false` (Go's `strconv.ParseBool`, so `1`, `t`, `T`,
`TRUE` and friends also work).

### The YAML subset

Every configuration value is a scalar, so mockulus parses a deliberately small
YAML: nested mappings, scalars, comments, quoted strings. Indent with spaces —
a tab in the indentation is refused by name rather than misread. Lists, anchors,
block scalars and multi-document files are **rejected with a line number**
rather than reinterpreted:

```console
$ mockulus --config ./bad.yaml
mockulus: parse config file ./bad.yaml: line 3: list values are not supported in mockulus configuration
```

Duplicate keys are an error, not a last-one-wins:

```console
$ mockulus --config ./dup.yaml
mockulus: parse config file ./dup.yaml: line 2: duplicate key "port"
```

And a key that is not a real key is an error, which is the one that saves the
most time in practice:

```console
$ mockulus --config ./typo.yaml
mockulus: config file ./typo.yaml: unknown key "admin_prot"
```

A configuration file that is silently ignored because of a typo is a deployment
running on defaults while its operator believes otherwise. Mockulus refuses to
be that deployment.

---

## Secrets

Three keys hold secrets: `admin_auth_token`, `couchbase.password` and
`tracing.headers`. All three accept a `_FILE` variant of their environment
variable, whose value is a **path** whose contents are the value:

```sh
MOCKULUS_ADMIN_AUTH_TOKEN_FILE=/var/run/secrets/mockulus/token
MOCKULUS_COUCHBASE_PASSWORD_FILE=/var/run/secrets/couchbase/password
MOCKULUS_TRACING_HEADERS_FILE=/var/run/secrets/otel/headers
```

This is for secrets that arrive as a mounted file — a Secret volume, a CSI
driver, a Vault agent sidecar — rather than as an environment variable. A
trailing newline is stripped, so a file written by `echo` works. The `_FILE`
form is consulted only when the plain variable is unset; setting both is not an
error; the plain one wins.

`_FILE` exists **only** for those three keys. `MOCKULUS_COUCHBASE_USERNAME_FILE`
is not a thing, and setting it does nothing at all — it is not a key, so nothing
reads it and nothing complains.

An unreadable path is a startup failure, not a fallback to empty:

```console
$ MOCKULUS_ADMIN_AUTH_TOKEN_FILE=/nope/token mockulus
mockulus: invalid environment configuration:
  - MOCKULUS_ADMIN_AUTH_TOKEN_FILE: open /nope/token: no such file or directory
```

### The startup dump, and what it hides

At `log.level: debug`, mockulus logs every resolved setting, one line per key,
before it does anything else. It is the only way to answer "what is this pod
actually running with" without guessing at the interaction of a file, an
environment and a set of defaults.

Secret-valued keys print as `[redacted]`. Given a file that sets
`journal_ttl: 10m` and `admin_auth_token`, run with
`MOCKULUS_JOURNAL_TTL=90s` in the environment:

```console
$ MOCKULUS_CONFIG=./demo.yaml MOCKULUS_JOURNAL_TTL=90s mockulus
time=2026-07-29T10:19:32.401+03:00 level=DEBUG msg=config setting="journal_enabled=true"
time=2026-07-29T10:19:32.401+03:00 level=DEBUG msg=config setting="journal_ttl=1m30s"
time=2026-07-29T10:19:32.401+03:00 level=DEBUG msg=config setting="admin_auth_token=[redacted]"
time=2026-07-29T10:19:32.401+03:00 level=DEBUG msg=config setting="log.level=debug"
time=2026-07-29T10:19:32.401+03:00 level=DEBUG msg=config setting="log.format=text"
```

`journal_ttl=1m30s` is the precedence rule made visible: the environment's `90s`
beat the file's `10m`, and the value is echoed in the canonical spelling it
would parse from.

The redaction is not cosmetic. Stub bodies are never logged either
([§14.2](../SPEC.md#142-logs)) — teams put real credentials in mocks, and a log
pipeline is a wider audience than a bucket.

---

## Validation: it refuses to start

Configuration is validated once, at load, before any port is bound. An invalid
configuration exits 1 with a message and starts nothing. There is no "warn and
continue", because a mock server that came up on a port nobody expected, with a
store nobody chose, is worse than one that did not come up.

Every problem is reported at once, so a deployment with three mistakes takes one
restart to fix rather than three:

```console
$ MOCKULUS_STORE=couchbase MOCKULUS_LOG_LEVEL=verbose \
  MOCKULUS_PORT=18740 MOCKULUS_ADMIN_PORT=18740 mockulus
mockulus: invalid configuration:
  - port and admin_port must differ (both 18740)
  - log.level: "verbose" is not one of debug, info, warn, error
  - store: couchbase requires couchbase.connstr
```

What validation covers:

- **Ports** are in range and the two listeners differ (`0` is legal on both — it
  asks the OS for an ephemeral port, which is how several instances share a
  machine).
- **Enumerated keys** — `store`, `templating_enabled`, `couchbase.durability`,
  `log.level`, `log.format` — must name one of their values, and the message
  lists the values.
- **Coherence** — `store: couchbase` requires `couchbase.connstr`; `store: file`
  requires `file.root`; `tracing.enabled` requires `tracing.endpoint`.
- **Bounds** — `sync_interval` has a 100 ms floor; `tracing.sample_ratio` must
  be between 0 and 1; durations and counts that must be positive are checked to
  be; sizes that may be zero-to-disable (`max_body_bytes`, `ephemeral_stub_ttl`,
  `journal_max_body`) are checked only for negativity.
- **The TLS key pair**, which is loaded from disk here rather than at the first
  handshake.

```console
$ MOCKULUS_SYNC_INTERVAL=50ms mockulus
mockulus: invalid configuration:
  - sync_interval: 50ms is below the 100ms minimum
```

That last one is worth dwelling on, because it is the difference between a pod
that fails and a pod that lies:

```console
$ MOCKULUS_TLS_CERT_FILE=/etc/tls/tls.crt MOCKULUS_TLS_KEY_FILE=/etc/tls/tls.key mockulus
mockulus: invalid configuration:
  - tls_cert_file/tls_key_file: open /etc/tls/tls.crt: no such file or directory
```

`ServeTLS` loads the pair on its own goroutine, long after the process has bound
its ports and reported itself ready — so a typo in a mounted path would
otherwise produce a pod that is live, ready, and serving nothing, which is the
worst available failure because Kubernetes routes traffic straight at it.

What validation does **not** cover: anything requiring the network. A wrong
`couchbase.connstr` is a valid configuration; it fails later, as a store that
will not connect, and [§4.4](../SPEC.md#44-startup-sequence) has the pod stay alive and not-ready while it retries.
A `file.root` that does not exist fails when the driver opens it.

---

## Choosing a store

`store` picks where stubs live. It is the one decision that changes what the rest
of the configuration is for.

| Value | What it is | Replicas |
|---|---|---|
| `auto` (default) | `couchbase` if `couchbase.connstr` is set, otherwise `memory` | — |
| `memory` | Everything in this process. Nothing survives a restart. | one |
| `couchbase` | Stubs, files, scenario state and journal in Couchbase; every replica reads the same data. | any |
| `file` | A WireMock project directory — `mappings/` and `__files/` — read-only. | one |

`auto` means the common cases need no `store` key at all: a laptop or a CI
sidecar gets `memory` by not configuring Couchbase, and a cluster gets
`couchbase` by configuring it. Setting `store` explicitly is worth doing when
you want a missing `couchbase.connstr` to be a startup failure rather than a
silent downgrade to a single-pod memory store — which, behind a Service with
three replicas, presents as stubs that work one request in three.

**`memory`** is the WireMock drop-in mode: one replica, no external dependency,
full admin API, scenarios and journal all working in-process. It is what the
zero-config start gives you and what most test suites want.

**`file`** points mockulus at a directory a team already has. Mappings are read
from `mappings/`, response bodies from `__files/`, exactly as WireMock reads
them. The directory is the source of truth, so **every admin write is refused**:

```console
$ curl -s -XPOST -d '{"request":{"method":"GET","url":"/x"},"response":{"status":200}}' \
    localhost:9090/__admin/mappings
{"errors":[{"code":1020,"title":"Store unavailable","detail":"the stub store is unavailable; the admin write was not applied"}]}
```

An in-process overlay was considered and rejected: it leaves the running server
disagreeing with the files the operator is editing, and the disagreement only
surfaces at the next restart, when the stub someone registered evaporates.

Edits on disk are picked up without a restart — the driver fingerprints the tree
(paths, sizes, mtimes) as its epoch, so a change converges through the same
reload path as any other, within `sync_interval`:

```console
$ curl -s localhost:8080/hello
hello from the file store
$ # edit mappings/hello.json, wait a second
$ curl -s localhost:8080/hello
edited on disk
```

Scenario state is the one thing the `file` driver keeps in memory, because it is
runtime state rather than project content and the serve path has to be able to
advance it.

The journal needs a store that can record one. `memory` and `couchbase` can;
`file` cannot, and asking for it is a startup failure rather than a surprise
later:

```console
$ MOCKULUS_STORE=file MOCKULUS_FILE_ROOT=./proj MOCKULUS_JOURNAL_ENABLED=true mockulus
mockulus: journal_enabled is set but the file store cannot record a journal
```

---

## Pointing at Couchbase

```yaml
store: couchbase
couchbase:
  connstr: couchbase://cb.mockulus.svc
  bucket: mockulus
  scope: _default
  username: mockulus
  # password via MOCKULUS_COUCHBASE_PASSWORD or MOCKULUS_COUCHBASE_PASSWORD_FILE
```

`connstr` is a `gocb` connection string; setting it is what turns `store: auto`
into `couchbase`. `bucket` defaults to `mockulus`; `scope` defaults to
`_default` and exists so that several team deployments can share one bucket by
taking a scope each.

**`manage_bucket`** (default `true`) has the driver create the missing scope,
the missing collections and the GSI index the journal's time-window queries
need, at boot. That is the low-configuration path and it is right for most
clusters. Set it `false` where the application user deliberately lacks manager
RBAC; then the DDL is applied once, out of band, by an init job. Despite its
name the key never creates the bucket — that is yours to provision either way.

**`durability`** (`none` by default, or `majority`) is how a write is
acknowledged. `none` is fast and test-oriented: a stub registration returns as
soon as one node has it in memory. Move to `majority` when mocks are long-lived
environment configuration rather than per-suite fixtures, and the cost of losing
a registration to a node failure exceeds the latency of replicating it.

It applies to scenario-state writes too, and those are on the request path — so
`majority` buys durability for stub registrations at the price of a slower
scenario transition. Plain stubs are untouched either way: serving one reads
nothing but the in-memory snapshot.

**`kv_timeout`** (2.5 s) and **`query_timeout`** (10 s) are the SDK budgets for
key-value operations and N1QL. They bound admin operations and snapshot loads.
Raise them only for a genuinely slow or distant cluster; they are not on the hot
path, so the usual reason to touch them is a load that is timing out during a
large reload rather than latency you are trying to shave.

**`scenario_kv_timeout`** (250 ms) is deliberately separate and much tighter,
because it *is* on the request path: a request against a scenario stub reads
state from Couchbase before it can decide whether the stub applies. A sick node
must not be able to stall a mock response for 2.5 seconds, so scenario reads get
their own quarter-second budget and a request that blows it fails with 500
`scenarioUnavailable` rather than guessing which side of a state machine it is
on. Requests that touch no scenario do zero KV operations and are unaffected.

### The convergence knobs

These are what make N replicas agree, and they only matter with a shared store.

**`sync_interval`** (1 s, floor 100 ms) is how often each pod reads the epoch
counter — one KV get of one document. Any change triggers a full snapshot
rebuild. This is the bound on cross-pod staleness: a stub registered against pod
A is visible on pod B within `sync_interval`. The pod that handled the write
sees it immediately, so a single-pod `stub → call → verify` flow has no
staleness at all.

Lower it if a suite writes on one connection and reads on another through a
Service and cannot tolerate a second. The cost is N pods × 1 read/s against
Couchbase, which is nothing; the floor exists because below 100 ms you are
paying rebuild coalescing overhead for a bound nobody can observe.

**`resync_interval`** (5 m) is an unconditional full reload that runs whether or
not the epoch moved. It sweeps stubs that expired by TTL — a TTL expiry is not a
mutation, so it does not bump the epoch — and it self-heals a signal that was
somehow missed. The explicit contract: an ephemeral stub that expired naturally
may keep matching on a pod for up to `resync_interval`, while explicit deletes
and resets propagate within `sync_interval`.

**`ephemeral_stub_ttl`** (24 h) is the lifetime of a stub registered without
`"persistent": true` — which is the default, and therefore most stubs. It is what
keeps a CI bucket from accumulating every stub every pipeline has ever
registered. `0` disables the TTL. Raise it if a suite legitimately leaves stubs
in place across a day; lower it on a busy shared cluster.

**`start_without_store`** (`false`) is an escape hatch, not a mode. Left off, a
pod whose store is unreachable at boot stays alive and not-ready and retries
forever with backoff — the Kubernetes-idiomatic answer, and the one that keeps a
Couchbase outage during a rollout from turning into a crash loop that outlasts
it. Turned on, the pod comes up ready and serves mock traffic from an empty
snapshot instead of waiting.

Understand what you are buying before you turn it on. Nothing the pod is told in
that state is durable, nothing reaches its peers, and the pod does not
re-attempt the store afterwards — a restart is what reconnects it. It is a way
to keep a suite moving through an outage at the cost of the guarantees a shared
store exists to provide.

---

## Turning on the journal

`journal_enabled` defaults to **`false`**, and that default is the reason this
project exists. WireMock journals every request by default; at 50k RPS that is
50k writes per second and the collapse mockulus was built to avoid. Off, the hot
path does no journal work whatsoever and the journal-dependent admin endpoints
answer WireMock's journal-disabled error.

Turn it on for suites that call `verify()`. Leave it off for load tests and for
any deployment serving traffic it does not intend to assert about.

```yaml
journal_enabled: true
journal_ttl: 30m
journal_max_body: 64KiB
```

Once on, a request on the mock port — matched or not — becomes an entry.
Everything else in the group bounds the cost of that.

**`journal_ttl`** (30 m) is how long an entry lives. The journal serves
functional tests, not analytics; 30 minutes outlives any suite that will
verify against it and keeps the collection from becoming a log store. Raise it if
you are debugging something that happened this morning; do not raise it to keep
history.

**`journal_max_body`** (64 KiB) caps the request body stored per entry, with the
truncation flagged on the entry. It is what stops one 8 MiB upload from being
persisted a thousand times.

**`journal_buffer`** (8192 entries) and **`journal_buffer_bytes`** (64 MiB) cap
the in-process queue between the request path and the writers — whichever limit
is hit first. The byte cap is the one that matters for a pod memory limit, since
8192 entries of unknown body size is not a number you can size a container
against. When the queue is full, entries are **dropped and counted**
(`mockulus_journal_dropped_total`), never blocked on: the hot path does not slow
down because the store is slow.

**`journal_flush_workers`** (4), **`journal_batch_size`** (500) and
**`journal_flush_interval`** (200 ms) tune the drain: four goroutines writing
batches of up to 500, flushed at least every 200 ms. These are throughput knobs,
and the sign that they need attention is a non-zero drop counter under a load
you consider reasonable. Raising workers and batch size trades Couchbase
connections for headroom.

**`journal_query_scan_limit`** (10000) bounds criteria queries — `count`, `find`,
`remove` — to the newest N entries. It is a guard rail against a verification
that accidentally asks the journal to behave like a database.

The visibility contract is [§11.4](../SPEC.md#114-consistency-contract): an entry is queryable within roughly the
flush interval plus index lag, typically under 500 ms. Verifications should use
the polling or timeout forms WireMock clients provide rather than asserting
immediately after the traffic.

---

## Turning on templating

`templating_enabled` takes three values, and the difference between them is
worth understanding because it decides what a literal `{{` in your mock data
does.

**`wm-compat`** (default) reproduces the activation rule of the pinned WireMock
3.13.2 ([§10.1](../SPEC.md#101-engine)): a stub is templated only when it declares the transformer, and a
stub without it serves its body byte for byte. Register the same body twice, once
each way:

```console
$ curl -s -o /dev/null -XPOST localhost:9090/__admin/mappings -d '
    {"request":{"method":"GET","url":"/plain"},
     "response":{"status":200,"body":"path is {{request.path}}"}}'

$ curl -s -o /dev/null -XPOST localhost:9090/__admin/mappings -d '
    {"request":{"method":"GET","url":"/tpl"},
     "response":{"status":200,"body":"path is {{request.path}}",
                 "transformers":["response-template"]}}'

$ curl -s localhost:8080/plain
path is {{request.path}}
$ curl -s localhost:8080/tpl
path is /tpl
```

**`on`** forces templating globally — every stub is templated whether or not it
asks. This is a mockulus extension, and it is convenient right up until a stub
returns a JSON document that happens to contain `{{`.

```console
$ MOCKULUS_TEMPLATING_ENABLED=on mockulus
$ # register the /plain stub above again — no transformer declared
$ curl -s localhost:8080/plain
path is /plain
```

**`off`** removes the engine from the process entirely. A stub that declares
`response-template` still registers — it is a valid mapping — but nothing
renders it, and it serves its template literally. Use it when you are certain no
stub needs templating and want the certainty that none can.

In every mode, a body or header value containing no `{{` is never touched, so
static stubs pay nothing.

**`template_max_output_bytes`** (10 MiB) caps a rendered result. Beyond it the
render fails, and a serve-time render failure puts the error text into the
response body — WireMock's own behavior — and counts
`mockulus_template_render_errors_total`. Registration-time failures are
different and stricter: a template that does not parse, or that calls a helper
outside the [§10.3](../SPEC.md#103-helper-allowlist-v1) allowlist, is refused with 422 when the stub is registered.
That includes `file`, `secret`, `systemValue` and `hostname`, which are excluded
deliberately — a template is sandboxed by construction, with no filesystem,
environment, network or system access at all.

---

## The admin API, and locking it

Three keys govern the admin surface.

**`admin_on_mock_port`** (`true`) serves `/__admin` on the mock port as well as
the admin port. WireMock clients assume this — they are configured with one base
URL and use it for both stubbing and traffic — so it is on by default for
compatibility. Turn it off when the mock port is reachable from beyond the
namespace, and drive stubbing through the admin port instead. With it off, a
request to `/__admin` on the mock port is an unmatched mock request like any
other:

```console
$ MOCKULUS_ADMIN_ON_MOCK_PORT=false mockulus
$ curl -s localhost:8080/__admin/mappings
No response could be served as there are no stub mappings in this mockulus instance.
```

That is the ordinary unmatched-request 404 of an empty instance — with stubs
loaded it reads `Request was not matched` instead. Either way the admin API is
not there, and the admin port still serves it.

**`admin_auth_token`** is unset by default, which means the admin API is open.
That is the right default for the expected posture — in-cluster only, behind a
NetworkPolicy — and the wrong one anywhere else. Set it and every `/__admin`
request must carry `Authorization: Token <value>`:

```console
$ curl -s -i localhost:9090/__admin/mappings
HTTP/1.1 401 Unauthorized
Content-Type: application/json

{"errors":[{"code":10,"title":"Malformed request","detail":"admin API requires a valid Authorization token"}]}

$ curl -s -o /dev/null -w '%{http_code}\n' \
    -H 'Authorization: Token s3cr3t' localhost:9090/__admin/mappings
200
```

What the token does and does not cover, verified on a running instance:

| Endpoint | With a token set |
|---|---|
| `/__admin/**` on either port | 401 without the token |
| `/debug/pprof/**` (admin port) | 401 without the token |
| `/healthz`, `/readyz` (admin port) | always open |
| `/metrics` (admin port) | always open |
| Mock traffic (mock port) | never gated |

The token guards the whole `/__admin` mux rather than individual routes, so a
route added later is protected by existing. It guards `/debug/pprof` because a
heap profile is a copy of every stub body the process is holding — exactly what
the token is protecting. It does not guard `/healthz`, `/readyz` or `/metrics`,
because the kubelet and Prometheus cannot present a token and none of the three
carries stub content.

Refusals are counted, so a deployment whose token is being guessed does not look
idle:

```console
$ curl -s http://localhost:9090/metrics | grep admin_requests_total | grep -v '^#'
mockulus_admin_requests_total{code="401",endpoint_group="mappings"} 1
```

**`admin_shutdown_enabled`** (`false`) controls `POST /__admin/shutdown`. Off, it
is refused like any other unsupported endpoint:

```console
$ curl -s -XPOST localhost:9090/__admin/shutdown
{"errors":[{"code":1001,"title":"Unsupported endpoint","detail":"/__admin/shutdown is not supported in mockulus v1 — see ROADMAP.md"}]}
```

On, it takes the same path a `SIGTERM` would: readiness drops, the drain window
elapses, the listeners close. It is useful for an ephemeral instance a test
harness owns end to end. Enabling it on anything shared hands every client of
the admin API a kill switch, which is why it is off and why it should stay off
wherever `admin_auth_token` is unset.

---

## TLS and HTTP/2

**`tls_cert_file`** and **`tls_key_file`** must be set together or not at all,
and they enable TLS on the **mock port only**. The admin port stays plaintext —
it is an in-cluster operations surface and the kubelet probes it. The minimum
version is TLS 1.2, stated on the listener rather than inherited from the
toolchain.

```console
$ MOCKULUS_TLS_CERT_FILE=./tls.crt MOCKULUS_TLS_KEY_FILE=./tls.key mockulus
$ curl -sk -o /dev/null -w '%{http_code} HTTP/%{http_version}\n' https://localhost:8080/nope
404 HTTP/2
```

Note the negotiated protocol: with TLS on, HTTP/2 comes with it via ALPN. Most
in-mesh deployments terminate TLS at the sidecar or the ingress and leave these
unset.

**`h2c_enabled`** (`false`) is cleartext HTTP/2 on the mock port, and the default
is a fidelity decision rather than a performance one. Fault injection —
`CONNECTION_RESET_BY_PEER`, `EMPTY_RESPONSE`, `MALFORMED_RESPONSE_CHUNK`,
`RANDOM_DATA_THEN_CLOSE` — works by hijacking the connection, which HTTP/2 does
not permit. Over h2c those faults degrade to a stream reset, which is not the
byte-level behavior the stub asked for. Off, the mock port speaks HTTP/1.1 and
faults are exact:

```console
$ curl -sS --http2-prior-knowledge http://localhost:8080/nope
curl: (55) Remote peer returned unexpected data while we expected SETTINGS frame.  Perhaps, peer does not support HTTP/2 properly.

$ MOCKULUS_H2C_ENABLED=true mockulus
$ curl -s -o /dev/null -w '%{http_code} HTTP/%{http_version}\n' \
    --http2-prior-knowledge http://localhost:8080/nope
404 HTTP/2
$ curl -s -o /dev/null -w '%{http_code} HTTP/%{http_version}\n' http://localhost:8080/nope
404 HTTP/1.1
```

Turn it on when a client under test insists on h2c and none of your stubs inject
faults. The same fidelity caveat applies to TLS-negotiated HTTP/2.

---

## The ports

**`port`** (8080) carries mock traffic, plus `/__admin` unless
`admin_on_mock_port` is off. **`admin_port`** (9090) carries `/__admin`,
`/healthz`, `/readyz`, `/metrics` and `/debug/pprof`. They must differ.

`0` on either asks the operating system for an ephemeral port, which is how
several instances coexist on one machine — a test harness reads the bound
address from the startup line:

```console
$ MOCKULUS_PORT=0 MOCKULUS_ADMIN_PORT=0 mockulus
{"time":"...","level":"INFO","msg":"mockulus started","version":"dev","store":"memory",
 "stubs":0,"load_ms":0,"mock_addr":"[::]:51531","admin_addr":"[::]:51530","admin_on_mock_port":true}
```

One consequence worth knowing: `mockulus -healthcheck`, the entrypoint the
image's `HEALTHCHECK` uses, probes `/healthz` on the configured admin port and
therefore refuses to run when `admin_port` is `0` — there is no fixed address to
aim at.

The listener timeouts are **not** configurable: `ReadHeaderTimeout` 10 s,
`IdleTimeout` 75 s, `MaxHeaderBytes` 1 MiB, and no write timeout on the mock
port, because a stub delay is a legitimate reason for a slow response.

---

## Logging

```yaml
log:
  level: info       # debug | info | warn | error
  format: json      # json | text
  requests: false
  request_sample_n: 100
```

`json` to stdout is the default and the right thing under a log collector;
`text` is for reading on a laptop. `debug` is what turns on the configuration
dump described above.

**`log.requests`** (`false`) is per-request access logging, and it is off because
it is on the hot path. On, with `log.request_sample_n` at its default of 100,
every hundredth request is logged:

```console
$ MOCKULUS_LOG_REQUESTS=true MOCKULUS_LOG_REQUEST_SAMPLE_N=1 mockulus
{"time":"...","level":"INFO","msg":"request served","method":"GET","path":"/sampled",
 "status":404,"matched":false,"stub":"","took_us":11,"sampled_of":1}
```

Sample 1 logs everything and is for a laptop or a failing case, not for a
deployment under load. Nothing in this path logs bodies or headers — stub
content may hold secrets a team put in a mock, so it stays out of the log
pipeline regardless of level.

**`metrics_enabled`** (`true`) exposes Prometheus metrics on `/metrics` on the
admin port. Turned off, the endpoint is gone entirely:

```console
$ MOCKULUS_METRICS_ENABLED=false mockulus
$ curl -s -w '\n%{http_code}\n' localhost:9090/metrics
404 page not found

404
```

There is rarely a reason to turn it off. The metric names are listed in
[SPEC §14.1](../SPEC.md#141-metrics-prometheus-metrics-on-admin-port).

**`ui_enabled`** (`true`) serves the embedded admin UI at
`/__admin/mockulus/ui/` on both listeners, and redirects the admin port's root
to it so that port-forwarding a pod and opening it in a browser lands somewhere
useful. Turned off, the whole prefix stops existing:

```console
$ MOCKULUS_UI_ENABLED=false mockulus
$ curl -s -w '\n%{http_code}\n' localhost:9090/__admin/mockulus/ui/
{"errors":[{"code":1001,"title":"Unsupported endpoint","detail":"/__admin/mockulus/ui/ is not supported in mockulus v1 — see ROADMAP.md"}]}
404
```

The redirect goes with it, so `/` on the admin port answers 404 as it did before
the UI existed. The admin API is untouched either way.

One property of the UI is worth knowing before you decide: its **static assets
are served without the `admin_auth_token` check**, and everything else —
including every call the UI makes — is checked as before. A browser cannot put
an `Authorization` header on a page load, so a token in front of the assets
would make the UI unreachable on exactly the deployments that set one. What is
exempt is code; the data behind it is not. The operator types the token into the
UI and it travels on the API calls, which are refused without it like any other.
[SPEC §5.7](../SPEC.md#571-the-admin-ui) states the amendment in full.

Note also that the admin listener has no TLS ([SPEC §12.1](../SPEC.md#121-listeners)),
so the UI is an in-cluster and port-forward tool. Putting it in front of anyone
else is an ingress decision.

---

## Tracing

`tracing.enabled` defaults to **`false`**, and that default is doing more than an
ordinary off switch does. Off, a served request loads one atomic pointer, finds
it nil, and takes the path it took before this feature existed: no span is
started, no request context is replaced, and nothing in the OpenTelemetry SDK is
constructed at all. It is the same shape the journal uses for the same reason,
and it is why the SLOs of
[§16.1](../SPEC.md#161-slos-release-criteria-for-v10-measured-on-the-reference-rig)
are stated for the default configuration. A deployment that turns tracing on is
choosing a different trade, on purpose.

Turn it on when the question you have is where the mock sits inside something
larger: which stub answered a call three services deep, whether anything matched
at all, how much of a slow request was the mock and how much was everything
around it. Leave it off for load tests, and for any deployment whose mock traffic
nobody is going to look at afterwards.

```yaml
tracing:
  enabled: true
  endpoint: otel-collector:4318
  insecure: true           # an in-cluster collector on a cleartext port
  sample_ratio: 0.1
  service_name: mockulus
```

The contract behind these keys is [§14.3](../SPEC.md#143-profiling--tracing);
what follows is what each one buys.

**`tracing.endpoint`** names the collector as `host:port`, and it is required
whenever tracing is on. It is deliberately not a URL. The transport is OTLP over
HTTP and the request path is fixed, so the only thing a URL would add is the
scheme — and `tracing.insecure` already decides that. An endpoint carrying one
anyway is a startup failure rather than a value quietly reinterpreted, because
two ways of saying the same thing are two things that can disagree:

```console
$ MOCKULUS_TRACING_ENABLED=true MOCKULUS_TRACING_ENDPOINT=http://otel-collector:4318 mockulus
mockulus: invalid configuration:
  - tracing.endpoint: "http://otel-collector:4318" must be host:port without a scheme; use tracing.insecure to choose http over https
```

Turning tracing on without naming a collector fails the same way, and the message
names the key you left out:

```console
$ MOCKULUS_TRACING_ENABLED=true mockulus
mockulus: invalid configuration:
  - tracing.endpoint: required when tracing.enabled is set
```

**`tracing.insecure`** (`false`) sends over plain HTTP instead of HTTPS. The
default is the safe one because that is the one you should have to opt out of —
but an OpenTelemetry Collector sitting in the same cluster, listening on `4318`,
is almost always cleartext, so `true` is the ordinary in-cluster value. Leave it
`false` for a collector outside the cluster, or one reached over any hop you do
not control end to end.

**`tracing.headers`** carries whatever the collector wants alongside the export
— typically an ingestion token for a hosted backend — as comma-separated pairs:

```sh
MOCKULUS_TRACING_HEADERS='x-api-key=abc123,x-tenant=payments'
```

The spelling is the one `OTEL_EXPORTER_OTLP_HEADERS` uses, so a value copied out
of another deployment's environment means the same thing here. It is a secret
key in the sense of the section above: it accepts the
`MOCKULUS_TRACING_HEADERS_FILE` form for a mounted secret, and it prints as
`[redacted]` in the startup dump.

```console
time=2026-07-29T18:02:38.107+03:00 level=DEBUG msg=config setting="tracing.endpoint=otel-collector:4318"
time=2026-07-29T18:02:38.107+03:00 level=DEBUG msg=config setting="tracing.insecure=true"
time=2026-07-29T18:02:38.107+03:00 level=DEBUG msg=config setting="tracing.headers=[redacted]"
time=2026-07-29T18:02:38.107+03:00 level=DEBUG msg=config setting="tracing.sample_ratio=0.1"
```

That redaction is the reason `tracing.headers` is one string rather than a
mapping: it is a single value the dump can hide whole, and an ingestion token is
the one thing in this group worth stealing. A value that is not a `k=v` pair is
refused at startup rather than silently dropped, which is what stops a header
typo from presenting as a collector that mysteriously rejects everything:

```console
$ MOCKULUS_TRACING_ENABLED=true MOCKULUS_TRACING_ENDPOINT=otel-collector:4318 \
    MOCKULUS_TRACING_HEADERS=oops mockulus
mockulus: invalid configuration:
  - tracing.headers: "oops" is not a key=value pair
```

**`tracing.sample_ratio`** (`0.1`) is the key worth understanding before you set
it, because it governs less than its name suggests. Sampling is **parent-based**:
a request arriving with a W3C `traceparent` follows the decision its caller
already made, whatever the ratio says. A test whose own trace is being sampled
therefore always gets the mock spans it caused, sitting inside its trace where
they belong; a caller that decided against sampling never makes mockulus pay for
spans nobody will read. The ratio applies only to the traces mockulus roots
itself — requests that arrive with no trace context at all.

That is what makes `0.1` a default rather than a compromise. The traffic you care
about is traced because your caller said so, and the anonymous traffic — a health
checker, a load generator, a client that does not propagate — contributes a tenth
of itself instead of turning "we switched tracing on" into an export storm on a
deployment serving tens of thousands of requests a second.

```console
$ # tracing on, sample_ratio: 0
$ curl -s -o /dev/null localhost:8080/hello
$ curl -s -o /dev/null \
    -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' \
    localhost:8080/hello
```

The collector receives exactly one span for those two requests: the second one,
with trace id `4bf92f3577b34da6a3ce929d0e0e4736` and the caller's span as its
parent. `0` is a legal and genuinely useful setting — it means "trace what my
callers ask you to trace, and nothing else". `1` traces everything, which is the
right value on a laptop and the wrong one under load.

**`tracing.service_name`** (`mockulus`) is the `service.name` every exported span
carries, and therefore the name your trace backend lists mockulus under. Change
it when one cluster runs several mockulus deployments and you need them apart —
`mockulus-payments`, `mockulus-checkout`. Two companion resource attributes are
not configurable: `service.version` is the build's version string, and
`service.instance.id` is the pod's name (`POD_NAME` when the environment sets it,
the hostname otherwise, which in Kubernetes is the same thing). Between them a
trace tells you not only that mockulus answered but which replica did.

### What a span carries

One server span per request, on both surfaces. Mock requests produce
`mock <METHOD>`; admin requests produce `admin <endpoint group>` — `admin
mappings`, `admin scenarios`, `admin mappings/reset`, the same grouping the
`mockulus_admin_requests_total` labels use.

The vocabulary is fixed on purpose, and for the same reason the metric labels of
[SPEC §14.1](../SPEC.md#141-metrics-prometheus-metrics-on-admin-port) are: span
names are what a trace backend groups by, so a name built from the request path
would let a 10k-stub deployment mint 10k span names the way a per-stub label
would mint 10k series. A method mockulus does not recognise is reported as
`_OTHER`, with the original carried as an attribute rather than promoted into the
name:

```console
$ curl -s -o /dev/null -X FROBNICATE localhost:8080/hello
$ # exported as: mock _OTHER, http.request.method=_OTHER, http.request.method_original=FROBNICATE
```

The attributes on a mock span:

| Attribute | Meaning |
|---|---|
| `http.request.method` | The method, or `_OTHER` for one outside the standard set |
| `http.request.method_original` | The method as sent — only present when the above is `_OTHER` |
| `url.path` | The request path |
| `http.response.status_code` | The status served |
| `mockulus.matched` | Whether a stub matched at all |
| `mockulus.stub.id` | The serving stub's id — absent when nothing matched |
| `mockulus.stub.name` | The serving stub's `name`, when it has one |
| `mockulus.snapshot.epoch` | The snapshot this pod served from |

`mockulus.matched` plus `mockulus.snapshot.epoch` is the pair that answers the
question this feature exists for: a 404 nobody can account for is either a stub
that was never registered or a pod that has not converged yet, and the epoch on
the span says which.

Admin spans carry `http.request.method`, `url.path`,
`mockulus.admin.endpoint_group` and `http.response.status_code`. The span wraps
authentication rather than sitting inside it, so a 401 is a span rather than a
gap — an operator investigating a locked-down deployment wants to find exactly
those requests, and a span that started after the refusal would never exist.

A 5xx marks the span as an error; a 4xx does not. An unmatched request answering
404 is mockulus working as specified, and a trace view that painted every one of
them red would be useless in the suites that produce them by the hundred.

No span name and no attribute carries a stub body or a header. That is the same
line the logs hold ([§14.2](../SPEC.md#142-logs)) and for the same reason: teams
put real credentials in their mocks, and a trace backend is a wider audience than
a bucket.

### The phases underneath a request

A server span says how long a request took. Its children say where that time
went, and they follow the same pay-per-use rule the phases themselves do — a span
appears only when the phase it measures actually ran, so a plain stub's trace is
a server span and a `match` and nothing else. The full model is
[SPEC §14.4](../SPEC.md#144-span-model--correlation):

| Child span | Appears when | Carries |
|---|---|---|
| `match` | always | `mockulus.candidates` — how many stubs were evaluated |
| `scenario.read` | the stub is in a scenario | `mockulus.scenario.name` |
| `scenario.transition` | the stub sets `newScenarioState` | `mockulus.scenario.name` |
| `template.render` | the stub is templated and templating is active | the span is marked failed on a render error |
| `delay` | the composed delay is above zero | `mockulus.delay_ms` |

The `delay` span is the one worth pointing at. A slow response and a response
that was *told* to be slow look identical in a duration, and only one of them is
a fault; with the span, the configured wait is visibly a separate block of time
from everything around it.

Two spans are deliberately **not** children of anything. `snapshot.rebuild` and
`journal.flush` root their own traces, because both are shared work: a rebuild is
coalesced across every write that triggered it and outlives all of them, and a
journal batch holds entries from many requests and often many traces. Attaching
either to one caller would bill the whole cluster's convergence to whoever
happened to arrive first.

### Finding the trace from a journal entry

When a request was sampled, its journal entry gains a `traceId` member and its
sampled access-log line gains a `trace_id` field. Both are how you get from
after-the-fact evidence back to the trace: the journal is usually where someone
starts, and an entry that cannot name its trace leaves them searching by
timestamp.

Both are absent rather than empty when there is no sampled span, so a deployment
that is not tracing keeps exactly the journal document and the log line it had.
`traceId` is an additive field, which is why it does not disturb the differential
comparison of [SPEC §5.6](../SPEC.md#56-differential-compatibility-verification-the-compat-tiebreaker).

`/__admin` served on the mock port never produces a mock span — it is excluded
there exactly as it is from the mock-port metrics, so `mockulus.matched` counts
stub traffic and nothing else. It still produces its admin span.

### Propagation, and the variables that are not read

Mockulus propagates W3C `tracecontext` only. Baggage is deliberately not
propagated: nothing here reads it, and a propagator that parses a header no code
consumes is surface without a purpose.

The standard `OTEL_*` environment variables — `OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_TRACES_SAMPLER` and the rest — are **not read**. This will surprise anyone
who has configured other OpenTelemetry services, so it is worth saying why: the
`tracing.*` keys are what the generated [§13](../SPEC.md#13-configuration-reference)
table documents, what startup validation checks, what the config dump prints and
redacts, and what the `_FILE` form covers. A second channel into the same
exporter would bypass all four, and a pod exporting somewhere its own
configuration dump does not mention is a bad afternoon. One mechanism owns every
knob.

Nothing about the export is on the request path. Spans go to a batching processor
and leave on a background exporter, so a slow or absent collector costs served
requests nothing but the spans it dropped. Dropped batches increment
`mockulus_trace_export_failures_total` and log their reason at most once a
minute; [`operations.md`](operations.md) has what to do about a non-zero one. At
shutdown the pending spans are flushed after the listeners have drained, so the
last requests a pod served are exported rather than discarded.

---

## Limits and timing

The remaining keys bound how much work one request can cause.

**`max_body_bytes`** (10 MiB) caps the request body read on the mock port.
Matching needs the whole body, so it is read fully into a pooled buffer;
anything larger is refused before that happens:

```console
$ MOCKULUS_MAX_BODY_BYTES=64 mockulus            # absurdly low, to show the refusal
$ curl -s -XPOST --data "$(python3 -c 'print("x"*200)')" localhost:8080/orders
{"errors":[{"code":1030,"title":"Request body too large","detail":"request body exceeds max_body_bytes"}]}
```

`0` removes the cap, which is a decision to let an untrusted caller decide your
memory ceiling. Raise the number instead, and size it against the container
limit.

**`regex_timeout`** (100 ms) bounds a single match on the fallback regex engine
— the one used for Java patterns RE2 cannot express, which is where
catastrophic backtracking lives. A timeout is treated as a non-match, counted as
`mockulus_regex_timeouts_total`, and logged with the offending pattern so it can
be found and fixed. Mock traffic is untrusted input; this is the ReDoS bound.

**`write_slack`** (10 s) is headroom on the per-response write deadline. A stub's
own delay is slept before the deadline is set, so a stub delaying five seconds
does not need this raised — `write_slack` is the budget for actually getting the
bytes out afterwards, plus the full `totalDuration` of a `chunkedDribbleDelay`.
Raise it only for a client so slow that legitimate writes are timing out.

**`diagnostics_on_unmatched`** (`false`) adds near-miss detail to the 404 a
request gets when nothing matched. It costs CPU on the miss path, which is why it
is off, and it is the fastest way to find out why a stub you are sure about is
not matching:

```console
$ MOCKULUS_DIAGNOSTICS_ON_UNMATCHED=true mockulus
$ # a stub on POST /orders requiring header X-Tenant: acme is registered
$ curl -s -H 'X-Tenant: other' -XPOST -d 'hi' localhost:8080/orders
Request was not matched
Closest stubs:

  7fd6a0fd-0ae5-4fff-9131-cde52963fb93
    header X-Tenant: expected equalTo "acme", got other
```

Turn it on in a development or CI deployment; leave it off under load.

**`shutdown_drain`** (5 s) and **`shutdown_timeout`** (15 s) shape the exit. On
`SIGTERM`, readiness flips to 503, mockulus waits `shutdown_drain` for that to
propagate to the Service endpoints, then closes both listeners with
`shutdown_timeout` as the ceiling on in-flight requests, flushes the journal and
closes the store. `shutdown_drain` should be at least your cluster's endpoint
propagation time; the chart pairs it with a `preStop` sleep for the same reason.
`shutdown_timeout` should exceed the longest delay any stub configures, or a
delayed response in flight is cut off.

---

## A complete file

Nothing here is required. This is what a shared, Couchbase-backed,
token-protected deployment looks like written out.

```yaml
port: 8080
admin_port: 9090
admin_on_mock_port: false        # mock port is exposed beyond the namespace

store: couchbase
couchbase:
  connstr: couchbase://cb.mockulus.svc
  bucket: mockulus
  scope: team-payments           # this deployment's slice of a shared bucket
  username: mockulus
  durability: majority           # mocks here are environment config, not fixtures
  manage_bucket: false           # DDL applied out of band by an init job

sync_interval: 1s
resync_interval: 5m
ephemeral_stub_ttl: 4h

journal_enabled: true
journal_ttl: 30m
journal_max_body: 32KiB

templating_enabled: wm-compat

log:
  level: info
  format: json
```

The two secrets stay out of the file and arrive as
`MOCKULUS_COUCHBASE_PASSWORD_FILE` and `MOCKULUS_ADMIN_AUTH_TOKEN_FILE`.

---

## In Kubernetes

The Helm chart in [`deploy/chart`](../deploy/chart) exposes these keys as
`config.*` values and renders them into `MOCKULUS_*` environment variables;
`config.extraEnv` passes through anything the chart does not name. Couchbase
credentials and the admin token come from a Secret via `secretKeyRef` rather
than the `_FILE` form — use `_FILE` when your secrets arrive as mounted files
instead.

[`values-hardened.yaml`](../deploy/chart/values-hardened.yaml) is the [§17](../SPEC.md#17-security) posture
as a preset: `admin_on_mock_port: false`, a NetworkPolicy, and a token that the
chart **refuses to render without**. That refusal is the point — forgetting the
`--set` would otherwise install a release that reads as locked down and has an
open admin API.

The chart also refuses to render more than one replica on top of the `memory` or
`file` driver — including via `autoscaling.maxReplicas` — which is the same class
of mistake caught at a different layer: a stub registered through the Service
would land on one pod and 404 on the others, at a rate set by the load balancer.

See [`deploy/chart/README.md`](../deploy/chart/README.md) for the values
reference and the scaling guidance of
[§15.4](../SPEC.md#154-scaling-guidance-documented-in-chart-readme), and
[`operations.md`](operations.md) for the topology, probe and degraded-mode
picture these keys sit inside.
