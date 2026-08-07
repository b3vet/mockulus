# Getting started

Mockulus is an HTTP mock server that speaks a defined subset of the [WireMock](https://wiremock.org)
3.x admin API and stub-mapping JSON format. It is built to run as N replicas behind a Kubernetes
Service, with stubs persisted in Couchbase and every mock request answered from an immutable
in-memory snapshot, so the request path never does I/O. Everything outside the supported subset is
refused when you register the stub, with a 422 that names the offending field — never accepted and
quietly ignored.

**Who this is for.** You already have a WireMock deployment or a test suite that drives one, and you
want it to survive horizontal scaling: dependency mocks under load tests, per-test stub registration
in CI, or a long-lived mock of a downstream service in a dev or staging cluster. If your mappings
use proxying, recording, webhooks, XML/XPath, or multipart matching, mockulus v1 will reject them —
read [The subset, and what a refusal looks like](#the-subset-and-what-a-refusal-looks-like) before
you plan a migration, not after, and then [Migrating from WireMock](migrating-from-wiremock.md) when
you do.

**Status.** Released. The current version is **v1.1.0**: multi-arch container images on
`ghcr.io/b3vet/mockulus`, static binaries for linux, macOS and Windows on the
[releases page](https://github.com/b3vet/mockulus/releases), a Helm chart, and
[`@mockulus/admin-sdk`](https://www.npmjs.com/package/@mockulus/admin-sdk) on npm for driving it from
TypeScript. Everything below also builds from a checkout. See [CHANGELOG.md](../CHANGELOG.md) for
what changed.

---

## Install

### Build from source

You need Go 1.25.4 or newer — the version pinned in `go.mod`.

```console
$ make build
go build  -trimpath -ldflags '-s -w -X main.version=v1.1.0' -o bin/mockulus ./cmd/mockulus

$ ./bin/mockulus -version
mockulus v1.1.0 (go1.25.4)
```

The version string is stamped from `git describe --tags --always --dirty` at build time, so an
untagged checkout reports its commit; a release build reports the tag.

There are exactly three flags. Everything else is configuration (see
[Configuring it](#configuring-it)):

```console
$ ./bin/mockulus -h
Usage of ./bin/mockulus:
  -config string
    	path to a YAML configuration file
  -healthcheck
    	probe this pod's own /healthz and exit 0 if it answers
  -version
    	print the version and exit
```

### Container image

Tagged releases publish multi-arch (amd64, arm64) images to `ghcr.io/b3vet/mockulus`. The image
is a distroless static base running as nonroot with no shell:

```console
$ docker run -d --name mockulus -p 8080:8080 -p 9090:9090 ghcr.io/b3vet/mockulus:v1.1.0
$ docker logs mockulus
{"time":"2026-08-07T10:40:27.866110671Z","level":"INFO","msg":"mockulus started","version":"v1.1.0","store":"memory","stubs":0,"load_ms":0,"mock_addr":"[::]:8080","admin_addr":"[::]:9090","admin_on_mock_port":true}
```

The tag carries the `v` — `:v1.1.0`, not `:1.1.0` — because it is the git tag the release was cut
from. `:latest` follows the most recent release. Static binaries for linux, macOS and Windows are
attached to each [GitHub release](https://github.com/b3vet/mockulus/releases) with a checksum file.

To build the image instead, `make image` uses the same Dockerfile the release pipeline does and
tags the result with the version string the binary reports.

The image carries a `HEALTHCHECK` that runs `mockulus -healthcheck`, which probes `/healthz` on the
configured admin port. The base image has no shell and no `curl`, so the binary is the only thing in
it that can make an HTTP request. Kubernetes should use the probes in
[SPEC §15.2](../SPEC.md#152-probes) instead.

---

## Start it with no configuration at all

```console
$ ./bin/mockulus
{"time":"2026-08-07T13:38:02.359919+03:00","level":"INFO","msg":"mockulus started","version":"v1.1.0","store":"memory","stubs":0,"load_ms":0,"mock_addr":"[::]:8080","admin_addr":"[::]:9090","admin_on_mock_port":true}
```

That single line is the whole startup summary, and it tells you what you got:

- **`store=memory`** — no Couchbase, no filesystem, no persistence. Stubs live in this process and die
  with it. This is the mode for a laptop, a unit test, or a single-replica drop-in replacement for a
  local WireMock.
- **`mock_addr=[::]:8080`** — the mock listener, carrying your stub traffic.
- **`admin_addr=[::]:9090`** — the admin and ops listener.
- **`admin_on_mock_port=true`** — `/__admin` is *also* served on 8080, because that is where a
  WireMock client library points by default.

Stopping it takes about five seconds: `SIGTERM` (or Ctrl-C) flips `/readyz` to 503, waits out the
`shutdown_drain` window — 5 s by default, sized so a Kubernetes Service has time to stop routing to
the pod — and only then closes the listeners.

```console
^C
{"time":"2026-07-29T10:31:48.652461+03:00","level":"INFO","msg":"shutting down"}
{"time":"2026-07-29T10:31:48.652559+03:00","level":"INFO","msg":"draining","duration":"5s"}
{"time":"2026-07-29T10:31:53.654956+03:00","level":"INFO","msg":"stopped"}
```

---

## Your first stub

(`Date` headers are elided from the responses in this document; everything else is verbatim.)

Nothing is registered yet, so every request on the mock port is a 404 that says so:

```console
$ curl -i http://localhost:8080/api/orders/42
HTTP/1.1 404 Not Found
Content-Type: text/plain;charset=UTF-8
Content-Length: 84

No response could be served as there are no stub mappings in this mockulus instance.
```

Register a mapping the way WireMock's admin API does it — `POST /__admin/mappings` with a
`request` half and a `response` half:

```console
$ curl -s -X POST http://localhost:8080/__admin/mappings \
    -H 'Content-Type: application/json' \
    -d '{
          "request":  { "method": "GET", "urlPath": "/api/orders/42" },
          "response": { "status": 200,
                        "headers": { "Content-Type": "application/json" },
                        "jsonBody": { "id": 42, "status": "SHIPPED" } }
        }'
{"id":"e80a9c24-90cf-4636-b202-032f2be11300","request":{"method":"GET","urlPath":"/api/orders/42"},"response":{"status":200,"headers":{"Content-Type":"application/json"},"jsonBody":{"id":42,"status":"SHIPPED"}},"uuid":"e80a9c24-90cf-4636-b202-032f2be11300"}
```

The response is `201 Created` and echoes the stub back with a server-assigned identity under both
`id` and `uuid` — they are aliases, and every run generates a different one. Supply your own `id` if
your suite needs to delete or replace the stub later by a name it chose.

Call it:

```console
$ curl -i http://localhost:8080/api/orders/42
HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 28

{"id":42,"status":"SHIPPED"}
```

One thing to note, because it differs from most servers: mockulus emits no `Content-Type` unless the
stub sets one. The header above is there because the mapping asked for it. A separate stub registered
as `{"request": {"method": "GET", "urlPath": "/ping"}, "response": {"status": 200, "body": "pong"}}`
answers with no content type at all:

```console
$ curl -i http://localhost:8080/ping
HTTP/1.1 200 OK
Content-Length: 4

pong
```

A request that matches nothing still gets a 404, now with the other diagnostic text — the snapshot is
no longer empty, so the message is about this request rather than about the instance:

```console
$ curl -i http://localhost:8080/api/orders/43
HTTP/1.1 404 Not Found
Content-Type: text/plain;charset=UTF-8
Content-Length: 23

Request was not matched
```

List what is registered:

```console
$ curl -s http://localhost:8080/__admin/mappings
{"mappings":[{"id":"e80a9c24-90cf-4636-b202-032f2be11300","request":{"method":"GET","urlPath":"/api/orders/42"},"response":{"status":200,"headers":{"Content-Type":"application/json"},"jsonBody":{"id":42,"status":"SHIPPED"}},"uuid":"e80a9c24-90cf-4636-b202-032f2be11300"}],"meta":{"total":1}}
```

That is the whole loop. A WireMock client library pointed at `http://localhost:8080` drives the same
endpoints and works unchanged — as long as the stubs it sends stay inside the subset.

### When a stub does not match

That bare `Request was not matched` is deliberately all you get by default: scoring every unmatched
request against every stub is exactly the cost this server exists to avoid, so unlike WireMock we do
not do it on the request path. Restart with `MOCKULUS_DIAGNOSTICS_ON_UNMATCHED=true` while you are
debugging and the near-miss detail is *appended* to the same body — same status, same content type,
same first line, so only the body distinguishes a debugging deployment from a production one:

```console
$ curl -i http://localhost:8080/api/orders/43
HTTP/1.1 404 Not Found
Content-Type: text/plain;charset=UTF-8
Content-Length: 132

Request was not matched
Closest stubs:

  159cbff1-033c-4917-b2d3-7686abe9668e
    url: expected /api/orders/42, got /api/orders/43
```

(The memory store came back empty across that restart, which is why the stub above carries a new id.)

Leave the flag off in production: with it off the unmatched path scores nothing and allocates
nothing. You can still ask the same question one request at a time without restarting anything —
`POST /__admin/near-misses/request` scores a request you describe against the current snapshot, at
admin-request time rather than on every miss:

```console
$ curl -s -X POST http://localhost:9090/__admin/near-misses/request \
    -H 'Content-Type: application/json' \
    -d '{"method": "GET", "url": "/api/orders/43"}' | jq '.nearMisses[0].matchResult'
{
  "differences": [
    {
      "kind": "url",
      "expected": "/api/orders/42",
      "actual": "/api/orders/43"
    }
  ],
  "distance": 0.03571428571428571
}
```

(`GET /__admin/requests/unmatched/near-misses` is the other one, but it reads the journal, so it
needs `journal_enabled` — see below.)

---

## The subset, and what a refusal looks like

Mockulus implements a strict subset of WireMock and **fails loudly on the rest**. A stub using an
unsupported feature is rejected when you register it, never accepted and quietly ignored. That is the
whole point of the rule: a stub that registers successfully and then silently never matches sends you
looking for the bug in your application, and the supported surface stays self-documenting because
anything outside it says so at the door.

Ask for XPath matching:

```console
$ curl -s -X POST http://localhost:8080/__admin/mappings \
    -H 'Content-Type: application/json' \
    -d '{
          "request":  { "method": "POST", "urlPath": "/api/orders",
                        "bodyPatterns": [ { "matchesXPath": "//order/id" } ] },
          "response": { "status": 201 }
        }' | jq .
{
  "errors": [
    {
      "code": 1000,
      "source": {
        "pointer": "/request/bodyPatterns/0/matchesXPath"
      },
      "title": "Unsupported feature",
      "detail": "matchesXPath (XPath matching) is not supported in mockulus v1 — see ROADMAP.md"
    }
  ]
}
```

That is `HTTP/1.1 422 Unprocessable Entity`, in WireMock's error envelope, with a JSON pointer at the
exact field and a pointer to [ROADMAP.md](../ROADMAP.md), where every deferred feature has a design
sketch and a bucket.

Every problem in the document is reported in one response — you fix them all in one round rather than
discovering them one 422 at a time:

```console
$ curl -s -X POST http://localhost:8080/__admin/mappings \
    -H 'Content-Type: application/json' \
    -d '{
          "request":  { "method": "GET", "urlPath": "/api/orders" },
          "postServeActions": [ { "name": "webhook" } ],
          "response": { "proxyBaseUrl": "http://the-real-service" }
        }' | jq .
{
  "errors": [
    {
      "code": 1000,
      "source": { "pointer": "/postServeActions" },
      "title": "Unsupported feature",
      "detail": "postServeActions (webhooks) is not supported in mockulus v1 — see ROADMAP.md"
    },
    {
      "code": 1000,
      "source": { "pointer": "/response/proxyBaseUrl" },
      "title": "Unsupported feature",
      "detail": "proxyBaseUrl (proxy mode) is not supported in mockulus v1 — see ROADMAP.md"
    }
  ]
}
```

**Nothing is persisted by a rejected write.** The one stub from the walkthrough is still all there is
after both of those calls — an invalid stub never enters the store, so it cannot reappear on the next
reload:

```console
$ curl -s http://localhost:8080/__admin/mappings | jq -c '.meta'
{"total":1}
```

An unsupported *endpoint* answers the same way with a 404 and code 1001:

```console
$ curl -s http://localhost:8080/__admin/recordings/status
{"errors":[{"code":1001,"title":"Unsupported endpoint","detail":"/__admin/recordings/status is not supported in mockulus v1 — see ROADMAP.md"}]}
```

### What is not here

Absent entirely, and rejected on sight: XML and XPath matching (`equalToXml`,
`matchesXPath`), multipart matching, proxying (`proxyBaseUrl`), record and playback, webhooks
(`postServeActions`), custom matchers, gRPC, browser proxying, Java-class extensions, and an admin
UI. The full field-by-field matrix is [SPEC §5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix);
the endpoint matrix is [§5.1](../SPEC.md#51-admin-api-endpoint-matrix).

Two defaults will surprise a WireMock user before anything else does, and both are deliberate:

- **The request journal is off.** WireMock records every request and you verify against that record.
  At 50k RPS that is 50k writes per second, which is the collapse this project exists to avoid, so
  it is opt-in. Until you set `journal_enabled`, every journal and verification endpoint answers:

  ```console
  $ curl -s http://localhost:9090/__admin/requests
  {"errors":[{"code":1010,"title":"Journal disabled","detail":"the request journal is disabled; set journal_enabled to record and verify requests"}]}
  ```

  A functional suite that calls `verify()` needs `journal_enabled: true`. A load test should leave it
  off.

- **Near-miss diagnostics on unmatched requests are off**, as described above.

Beyond the outright absences there are 47 catalogued, deliberate behavioural differences from the
pinned WireMock 3.13.2 — each one named, justified, and in most cases carrying a knob to restore
WireMock's behaviour. They are written up in [Deviations from WireMock](deviations.md), and stated
normatively in [SPEC §5.5](../SPEC.md#55-deviations-from-wiremock-complete-list-v1). Read one of them
if you are moving an existing mappings set across; skip both if you are starting fresh.

---

## Two ports, and why there are two

| Port | Serves | Expose to |
|---|---|---|
| `8080` (`port`) | Mock traffic, plus `/__admin` unless you turn that off | Whatever is under test |
| `9090` (`admin_port`) | `/__admin`, `/healthz`, `/readyz`, `/metrics`, `/debug/pprof` | Your namespace and your monitoring |

WireMock client libraries assume the admin API lives on the same port as the mocks, so it does —
that is what makes an existing suite work unchanged. But that leaves nowhere to put the endpoints
you do *not* want reachable from wherever the mock port is exposed, which is why there is a second
listener. Health, readiness, metrics and pprof are on 9090 only:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/healthz
404
$ curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/metrics
404
```

Those are 404s from the mock listener because nothing matched them — to it they are ordinary requests
with no stub behind them. When the mock port is exposed beyond the namespace, set
`admin_on_mock_port: false` and drive the admin API over 9090 alone.

The admin API is **open by default**. The expected production posture is a NetworkPolicy plus
in-cluster-only access; where that is not enough, `admin_auth_token` requires
`Authorization: Token <t>` across the whole `/__admin` mux and `/debug/pprof` — a heap profile is a
copy of every stub body the process is holding. `/healthz`, `/readyz` and `/metrics` stay
unauthenticated whatever the token setting, because the kubelet and Prometheus cannot present one.
See [SPEC §17](../SPEC.md#17-security).

---

## Health, readiness and metrics

```console
$ curl -i http://localhost:9090/healthz
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 3

ok

$ curl -i http://localhost:9090/readyz
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 6

ready
```

Plain text and nothing else, deliberately — these are read by a kubelet, not by a person. The two
answer different questions, and the difference matters when you wire up probes:

- **`/healthz`** — the process is up. It never depends on the store, because a Couchbase outage must
  not restart your pods. Use it as the liveness probe.
- **`/readyz`** — a valid snapshot is loaded and the listeners are bound. It is 503 during the
  initial load and stays 200 through a store outage, since the last snapshot is still servable. Use
  it as the readiness probe.

You can watch the difference: point mockulus at a Couchbase that is not there and it binds the admin
listener immediately, answers `healthz` 200, `readyz` 503 `not ready`, and does not bind the mock
port at all until the store connects. That is deliberate — a pod that stops answering its liveness
probe while waiting for a database is a pod Kubernetes restarts into a backoff that outlasts the
outage.

`/__admin/health` gives the same verdict with detail, in WireMock's shape plus fields of our own:

```console
$ curl -s http://localhost:9090/__admin/health | jq .
{
  "epoch": 1,
  "message": "mockulus is ok",
  "status": "healthy",
  "store": {
    "driver": "memory"
  },
  "stubs": 1,
  "timestamp": "2026-08-07T10:40:08.812932Z",
  "uptimeInSeconds": 9,
  "version": "v1.1.0"
}
```

`/__admin/version` is what a WireMock client asks for during handshake:

```console
$ curl -s http://localhost:9090/__admin/version | jq .
{
  "goVersion": "go1.25.4",
  "guessedWireMockVersion": "3.x-subset",
  "version": "v1.1.0"
}
```

Metrics are Prometheus text on the admin port. They are low-cardinality on purpose — no per-stub
labels, so a 10,000-stub deployment does not mint 10,000 series. After one matched request and one
that missed, the interesting few read:

```console
$ curl -s http://localhost:9090/metrics \
    | grep -E '^mockulus_(build_info|http_requests_total|snapshot_epoch|snapshot_stubs)'
mockulus_build_info{go_version="go1.25.4",version="v1.1.0"} 1
mockulus_http_requests_total{code="200",matched="true"} 1
mockulus_http_requests_total{code="404",matched="false"} 1
mockulus_snapshot_epoch 1
mockulus_snapshot_stubs 1
```

`matched` is the label to alert on: a suite whose stubs stopped matching shows up as
`mockulus_http_requests_total{matched="false"}` climbing, well before anyone reads a test report.
[Operating mockulus](operations.md) covers what else to watch and what to do about it; the full
metric list is [SPEC §14.1](../SPEC.md#141-metrics-prometheus-metrics-on-admin-port).

---

## Where your stubs live

The zero-config start uses the `memory` store, which is fine for a laptop and useless for a cluster.
There are three drivers, selected by `store` (default `auto`: Couchbase if a connection string is
set, otherwise memory).

**`memory`** — in-process, no persistence, single replica. The unit-test and local-development
default.

**`file`** — reads a WireMock project directory, `mappings/` plus `__files/`, which is the fastest
way to find out whether the stubs you already have are inside the subset. Point it at the directory
and nothing else changes; the run below has one mapping in `mappings/orders.json` referencing
`__files/order-42.json` through `bodyFileName`:

```console
$ MOCKULUS_STORE=file MOCKULUS_FILE_ROOT=/path/to/wiremock-project ./bin/mockulus
{"time":"2026-08-07T13:41:04.948264+03:00","level":"INFO","msg":"mockulus started","version":"v1.1.0","store":"file","stubs":1,"load_ms":0,"mock_addr":"[::]:8080","admin_addr":"[::]:9090","admin_on_mock_port":true}

$ curl -i http://localhost:8080/api/orders/42
HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 32

{"id": 42, "status": "SHIPPED"}
```

The bytes come from `__files/order-42.json` exactly as they sit on disk — the file is read into
memory when the snapshot is built, never on the request path.

`stubs=1` in that startup line is the number worth reading: compare it against the number of files in
`mappings/`. A mapping that does not compile is quarantined rather than aborting the load, so the
rest of the directory still serves and the shortfall is logged, one warning per stub:

```
{"time":"2026-07-29T10:29:26.687432+03:00","level":"WARN","msg":"stub quarantined: mapping does not compile","id":"7c6df22a-0230-5779-9135-1b73d0b71683","problems":1}
```

That line counts the problems but does not name them, and
`mockulus_snapshot_quarantined_total{reason="compile"}` only totals them. To find out *which* field
a quarantined mapping tripped over, `POST` it to a throwaway `memory`-store instance and read the
422 — the same compiler runs on both paths, so the pointers you get back are the ones that
quarantined it.

Edits to the directory are picked up on the next reload, within `sync_interval` (1 s by default),
with no restart.

The directory is the source of truth, which makes this driver read-only. Admin writes are refused:

```console
$ curl -s -X POST http://localhost:8080/__admin/mappings \
    -H 'Content-Type: application/json' \
    -d '{"request":{"method":"GET","urlPath":"/x"},"response":{"status":200}}'
{"errors":[{"code":1020,"title":"Store unavailable","detail":"the stub store is unavailable; the admin write was not applied"}]}
```

That is a 503, and the body is the generic one every store that cannot take a write returns. The
store is not actually broken; the server log for that request is the line that says why:

```
{"level":"ERROR","msg":"store operation failed","op":"next_seq","error":"store unavailable: the file store serves a WireMock project directory read-only; edit the mappings and restart or wait for the reload"}
```

An in-process overlay was the alternative and was rejected: it leaves the running deployment
disagreeing with the files the operator is editing, and the disagreement only surfaces at the next
restart, when the stub someone registered evaporates.

**`couchbase`** — the production driver, and the only one that makes several replicas coherent.
Stubs are persisted, an epoch counter tells every pod when to reload, and a registration on one
replica is visible on the others within `sync_interval`. Set `couchbase.connstr`, `couchbase.username`
and `couchbase.password` (the password also reads from a `MOCKULUS_COUCHBASE_PASSWORD_FILE` mount) and
the `auto` store selects it. See [SPEC §7](../SPEC.md#7-storage--data-model) for the layout and
[§8](../SPEC.md#8-cluster-synchronization) for how propagation works.

---

## Configuring it

Every setting has an environment variable — `MOCKULUS_` plus the upper-snake key — and a YAML
equivalent for `--config`. Precedence is env var, then file, then default. Durations use Go syntax
(`1s`, `200ms`), sizes take `KiB`/`MiB` suffixes.

The handful you are most likely to touch first:

| Key | Env var | Default | What it does |
|---|---|---|---|
| `port` | `MOCKULUS_PORT` | `8080` | Mock listener (`0` binds an ephemeral port) |
| `admin_port` | `MOCKULUS_ADMIN_PORT` | `9090` | Admin/ops listener |
| `admin_on_mock_port` | `MOCKULUS_ADMIN_ON_MOCK_PORT` | `true` | Also serve `/__admin` on the mock port |
| `store` | `MOCKULUS_STORE` | `auto` | `auto` \| `couchbase` \| `memory` \| `file` |
| `journal_enabled` | `MOCKULUS_JOURNAL_ENABLED` | `false` | Required by `verify()` and every `/__admin/requests` endpoint |
| `diagnostics_on_unmatched` | `MOCKULUS_DIAGNOSTICS_ON_UNMATCHED` | `false` | Near-miss detail in 404 bodies |
| `admin_auth_token` | `MOCKULUS_ADMIN_AUTH_TOKEN` | — | Require `Authorization: Token <t>` on `/__admin` and pprof |
| `log.format` | `MOCKULUS_LOG_FORMAT` | `json` | `text` is easier to read locally |

Every key, what it costs and what it breaks is in [Configuration](configuration.md); the normative
table, generated from the config struct so it cannot drift from the code, is
[SPEC §13](../SPEC.md#13-configuration-reference).

---

## Cleaning up between tests

| Call | Removes |
|---|---|
| `DELETE /__admin/mappings` | Every stub, persistent or not |
| `POST /__admin/mappings/reset` | Non-persistent stubs only |
| `POST /__admin/reset` | The above, plus the journal and every scenario back to `Started` |

```console
$ curl -s -X POST http://localhost:8080/__admin/reset
{}
$ curl -s http://localhost:8080/__admin/mappings
{"mappings":[],"meta":{"total":0}}
```

**One deployment is one namespace for stubs, scenarios and the journal.** There is no in-app
multi-tenancy in v1. If several CI runners share a deployment, keep their stubs distinguishable with
unique URLs and scenario names, tag them via `metadata` (`{"suite": "<run-id>"}`) and clean up with
`POST /__admin/mappings/remove-by-metadata` — and do **not** call the global resets above, which
destroy every other runner's stubs at the same time. A runner that needs real isolation gets its own
instance: single replica, `memory` store, no Couchbase at all.

---

## Two things you did not have to install

Everything above is `curl`, because that is the lowest common denominator and it proves the API is
just HTTP. Neither of the following is a separate deployment.

**A browser interface, compiled into the binary.** Open the admin port and it is there:

```console
$ curl -s -o /dev/null -w '%{http_code} -> %{redirect_url}\n' http://localhost:9090/
302 -> http://localhost:9090/__admin/mockulus/ui/
```

Browse and edit stubs in a JSON editor that puts the cursor on each field a refusal names, read the
journal, ask why a request did not match, drive scenarios. It is served on the mock port at the same
path for deployments that expose only that one, and `MOCKULUS_UI_ENABLED=false` removes the routes
outright. It lives under `/__admin/mockulus/**`, a namespace reserved for mockulus' own endpoints
that WireMock answers `404` for, so no WireMock client can collide with it.
See [The admin UI](admin-ui.md).

**A typed client, if you drive mocks from TypeScript.**

```console
$ npm install @mockulus/admin-sdk
```

It types the supported subset and nothing else, so a stub the server would refuse with a `422` is a
compile error instead. Any existing WireMock client library also works unchanged against the same
API — this is for people who would rather not hand-roll the calls. See
[Programmatic administration](programmatic-administration.md).

---

## What to read next

| If you want to | Read |
|---|---|
| Move an existing WireMock deployment across, in the order you actually do it | [Migrating from WireMock](migrating-from-wiremock.md) |
| Know every place mockulus answers differently, and what to do about it | [Deviations from WireMock](deviations.md) |
| Set any of this up properly — every key, what it costs, what it breaks | [Configuration](configuration.md) |
| Run it: deployment shapes, the chart, Couchbase, probes, what to alert on | [Operating mockulus](operations.md) |
| Use the browser interface that ships in the binary | [The admin UI](admin-ui.md) |
| Drive it from code, with a typed client or your own | [Programmatic administration](programmatic-administration.md) |
| Find out what is deferred, and the sketch for how it will work | [ROADMAP.md](../ROADMAP.md) |
| Contribute a change | [CONTRIBUTING.md](../CONTRIBUTING.md) |

Underneath all of those is [SPEC.md](../SPEC.md), which is the authoritative contract rather than a
summary of it, and is written to be read. The sections you will reach for:

| Question | Section |
|---|---|
| Exactly which endpoints and stub fields work | [§5.1](../SPEC.md#51-admin-api-endpoint-matrix), [§5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix) |
| How a request is matched, and which stub wins when several do | [§6](../SPEC.md#6-matching-engine), [§5.3](../SPEC.md#53-matching--selection-semantics) |
| Response templating and the helper allowlist | [§10](../SPEC.md#10-response-templating) |
| Stateful mocks | [§9](../SPEC.md#9-scenarios-stateful-mocks) |
| Recording requests and verifying them | [§11](../SPEC.md#11-request-journal--verification) |
| Delays, faults and the HTTP layer | [§12](../SPEC.md#12-http-layer) |
| What happens when Couchbase goes away | [§4.6](../SPEC.md#46-degraded-modes-explicit-contract) |
| The throughput and latency targets v1.0 has to clear, and the rig they are measured on | [§16.1](../SPEC.md#161-slos-release-criteria-for-v10-measured-on-the-reference-rig) |

Mockulus is not affiliated with, endorsed by, or sponsored by the WireMock project or WireMock Inc.;
the WireMock name is used here solely to describe API compatibility.
