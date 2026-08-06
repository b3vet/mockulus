# Migrating from WireMock

This is a practical guide for a team that already runs WireMock 3.x and has a
directory of mappings, a CI suite that talks to it, or both. It walks the move
in the order you actually do it: find out what your mappings use, understand
what changes about the deployment, load the mappings, repoint the clients, turn
on the two things that are off by default, and then check the handful of
deviations that can change a green suite into a red one.

Two facts shape everything below.

**Mockulus implements a subset.** XML and XPath matching, proxying, recording,
webhooks, multipart matching and Java-class extensions are not implemented. They are catalogued with design sketches in
[ROADMAP.md](../ROADMAP.md). The date-time matchers `before`, `after` and
`equalToDateTime` **are** implemented — see the note on them below, because their
comparison rule is less obvious than it looks.

**Nothing in that subset is silently ignored.** A stub using an unsupported
field is refused at registration with `422` and an error body whose
`source.pointer` names the field. An unsupported admin path is `404` with code
`1001`. This is the property that makes the migration tractable: you learn about
every gap in one command, before a test runs, instead of at three in the morning
when a stub that registered fine turns out never to match.

The full behavior-by-behavior surface is the [compatibility
matrix](compatibility.md) and the complete list of differences is
[Deviations from WireMock](deviations.md); this guide is the route through them.
Every configuration key it names is described in [Configuration](configuration.md),
and the deployment shapes it sketches are worked through in
[Operating mockulus](operations.md).

---

## What does not change

Your mapping JSON, for everything in the subset. The admin API is
wire-compatible, so a WireMock client library pointed at a remote host works
unchanged: `WireMock.configureFor(host, port)` and the same `stubFor` and
`verify` calls, or whatever the equivalent is in your language. Stub selection
is the same: priority ascending, then insertion order descending, so the most
recently added stub wins a tie. Scenarios are the same state machine with the
same `Started` initial state. Response templating is the same Handlebars
dialect, over an allowlisted subset of the helpers.

## What changes

One stateful JVM becomes N stateless replicas behind a Kubernetes Service, with
Couchbase as the source of truth. That is the point of the project, and it is
also the source of every operational difference in this guide: scenario state
and the request journal are no longer JVM heap, they are documents in a shared
store, and a stub registered on one replica becomes visible on the others after
a bounded delay rather than instantly.

---

## Step 1 — Assess: import your mappings and read the 422s

Read the [compatibility matrix](compatibility.md) if you want to know the answer
in advance. The faster route is empirical, because the server will tell you.

Start a throwaway instance. It needs no Couchbase and no configuration; the
default store is in-memory. The examples here use ports 18411 and 18511 so the
instance can sit next to an existing WireMock on 8080:

```sh
make build
MOCKULUS_PORT=18411 MOCKULUS_ADMIN_PORT=18511 ./bin/mockulus &
```

The startup line reports the version, the store driver, how many stubs loaded
and how long the load took. With no store configured it says `store=memory
stubs=0`.

### Collate the mappings directory

`POST /__admin/mappings/import` takes one document containing every mapping.
A WireMock `mappings/` directory holds a mix of single-mapping files and files
carrying a `{"mappings": [...]}` envelope, so flatten both forms:

```sh
jq -s '{mappings: [ .[] | if has("mappings") then .mappings[] else . end ]}' \
   mappings/*.json > import.json
```

### Import it, and read the pointers

```sh
curl -s -X POST http://localhost:18411/__admin/mappings/import \
     -H 'Content-Type: application/json' \
     --data-binary @import.json | jq .
```

For a six-mapping suite carrying a SOAP stub, a multipart stub and a webhook,
that answers `422` with:

```json
{
  "errors": [
    {
      "code": 1000,
      "source": { "pointer": "/mappings/0/postServeActions" },
      "title": "Unsupported feature",
      "detail": "postServeActions (webhooks) is not supported in mockulus v1 — see ROADMAP.md"
    },
    {
      "code": 1000,
      "source": { "pointer": "/mappings/2/request/bodyPatterns/0/equalToXml" },
      "title": "Unsupported feature",
      "detail": "equalToXml (XML matching) is not supported in mockulus v1 — see ROADMAP.md"
    },
    {
      "code": 1000,
      "source": { "pointer": "/mappings/5/request/multipartPatterns" },
      "title": "Unsupported feature",
      "detail": "multipartPatterns is not supported in mockulus v1 — see ROADMAP.md"
    }
  ]
}
```

That is the assessment. Three mappings out of six need a decision; the pointers
give the array index and the exact field, and the index maps back to a file
through the order `jq` collated them in.

Every 422 lists **all** the problems it found, rather than failing on the first
one, so this is one round trip and not one per broken mapping.

### The import is atomic

Nothing was written. `GET /__admin/mappings` still reports `{"total": 0}` even
though three of the six mappings were perfectly valid. The batch is validated in
full before anything is persisted, which means you can run the import repeatedly
while you work through the list without ending up with a half-loaded deployment.
(WireMock applies a failing import partially — SPEC §5.5 deviation 21.)

### Use import for the assessment, not the file store

There is a `file` store driver that reads a WireMock layout directly:

```sh
MOCKULUS_STORE=file MOCKULUS_FILE_ROOT=/path/to/suite ./bin/mockulus
```

It is useful for local development — `mappings/` and `__files/` work as they do
under WireMock, `bodyFileName` resolves against `__files/`. It is the wrong tool
for the assessment, because a mapping it cannot compile is **quarantined**
rather than refused: excluded from the snapshot with a WARN log and a
`mockulus_snapshot_quarantined_total{reason="compile"}` increment, but no
per-field detail:

```
level=WARN msg="stub quarantined: mapping does not compile" id=7c6df22a-0230-5779-9135-1b73d0b71683 problems=1
```

That behaviour is deliberate — one bad document must never freeze stub
propagation for a whole cluster, least of all during a rolling upgrade — but it
tells you a stub is broken without telling you why. The import path is the one
that names the field.

A dry-run validator that reports the same list without a running server is on
the roadmap as part of `mockulusctl` (ROADMAP 3.3). Until it exists, the
throwaway instance is the validator.

### What the error codes mean

An assessment turns up two kinds of problem, and they call for different work.

| Code | HTTP | Meaning |
|---|---|---|
| `1000` | 422 | The field is a real WireMock feature that v1 does not implement. The `detail` points at the roadmap. |
| `10` | 422 | The document is malformed, or it is a document WireMock would have silently coerced or half-honoured. |
| `1002` | 422 | Unknown template helper, or a template that does not parse. |
| `1003` | 422 | A regex that compiles on neither engine. |
| `1004` | 422 | An unknown `transformers` entry. |
| `1005` | 422 | An unknown key in `POST /__admin/settings`. |
| `1001` | 404 | An admin endpoint that does not exist here (`/__admin/recordings/**`, `/__admin/proxy/**`, `/__admin/certificates/**`). |

Code `1000` is a scope decision — you wait for the roadmap, or you rewrite the
stub. Code `10` is usually a five-minute fix in your mapping, and the ones you
are most likely to hit are listed under [step 6](#step-6--the-deviations-most-likely-to-change-your-suite).

---

## Step 2 — The deployment shape

WireMock is one process holding everything in one heap. Mockulus is N replicas
holding a read-only copy of the stubs each, with the writable state in
Couchbase. Requests are served entirely from the in-memory copy: matching never
touches the network.

### Where the state lives now

| State | WireMock 3 | Mockulus |
|---|---|---|
| Stub mappings | JVM heap, optionally seeded from `mappings/` | `mappings` collection; compiled into a per-pod immutable snapshot |
| `__files` bodies | Filesystem next to the process | `files` collection; inlined into the snapshot at build time |
| Scenario state | JVM heap | `scenarios` collection; one KV read per matching scenario stub, transitions by CAS |
| Request journal | JVM heap | `journal` collection; async batched writes, entries expire after `journal_ttl` (30 m) |
| Global delay settings | JVM heap | `meta::settings`; compiled into the snapshot, so it survives restarts and applies cluster-wide |

Two consequences follow, and both are contracts rather than accidents.

**Stub propagation is bounded, not instant.** The replica that handled the admin
write applies it to its own snapshot immediately, so a single-pod flow of
`register stub → call it → assert` sees no staleness at all. Other replicas pick
it up on their next epoch poll, within `sync_interval` (default `1s`). If your
suite registers a stub through a Service and immediately calls the mock through
the same Service, the call can land on a replica that has not converged yet.
Retry, or run the suite against a single replica.

**Verification is eventually consistent.** Journal entries are flushed in
batches every `journal_flush_interval` (default `200ms`) and then have to become
visible to the index. Use the polling or timeout forms your WireMock client
already provides (`verify(…)` with a wait, `await`-style helpers) rather than a
bare assertion on the request immediately after the call.

Scenario state is the one place where correctness is chosen over availability:
if Couchbase is unreachable, a request that matches a scenario-gated stub gets
`500` with code `1021` rather than a guess. Plain stubs keep being served from
the snapshot throughout an outage; admin writes answer `503` with code `1020`.

### One deployment is one namespace

There is no in-app multi-tenancy in v1. A deployment has one set of stubs, one
set of scenario names and one journal. Concurrent CI runners sharing a
deployment have to keep their stubs distinguishable — unique URLs, unique
scenario names — and tag them so they can clean up after themselves:

```sh
curl -s -X POST http://localhost:18411/__admin/mappings \
  -H 'Content-Type: application/json' \
  -d '{"metadata":{"suite":"orders-ci-8841"},
       "request":{"urlPath":"/api/orders"},"response":{"status":201}}'
```

```sh
curl -s -X POST http://localhost:18411/__admin/mappings/remove-by-metadata \
  -H 'Content-Type: application/json' \
  -d '{"matchesJsonPath":{"expression":"$.suite","equalTo":"orders-ci-8841"}}'
```

Unlike WireMock, `remove-by-metadata` returns the mappings it removed under the
standard list envelope, so the cleanup step can log what it did.

What a shared runner must **not** do is call a global reset. Against a deployment
holding one `persistent: true` stub and two ordinary ones, one of them in a
scenario that had already moved to `moved`, with one journal entry recorded:

```
before reset: {"total":3}  scenario states ["moved"]  journal {"total":1}
POST /__admin/reset -> 200
after  reset: {"total":1}  scenario states []         journal {"total":0}
```

Every non-persistent stub in the deployment is gone, including the ones another
runner registered thirty seconds ago; every scenario is back to `Started`; the
journal is empty. `POST /__admin/mappings/reset` does the stub half alone.
`DELETE /__admin/mappings` goes further still and takes the persistent stubs
with it, leaving `{"total": 0}`.

A test framework that calls `reset()` in an `@BeforeEach` is doing this on every
test. Under WireMock, where each suite typically owns a process, that is
harmless. Here it is a cross-runner outage. Either tag and remove by metadata,
or give the runner its own instance.

### The on-ramp: single replica, memory store

`replicaCount: 1` with the default in-memory store is a drop-in WireMock
replacement with no Couchbase anywhere, and it removes the cross-replica
propagation delay entirely: registering a stub and calling it on the next line
works, because there is only ever the one snapshot to update.

```
register c73601fe-f97c-45e0-a280-9d6a158490a0 -> immediate GET: ok1
register a73585ec-6a87-4edd-ae13-a1e4324c33fe -> immediate GET: ok2
register 081049d2-51a6-42ed-b619-7ed0ad54210f -> immediate GET: ok3
```

`verify()` works in this mode too — the journal uses whichever store driver is
configured, so a memory-store instance journals to memory. What it does **not**
remove is the journal's flush window: entries are still batched, so six requests
fired in a loop read back as `{"count": 1}` until the flusher runs and then as
`{"count": 6}`. Verification needs the polling form on one replica exactly as it
does on ten.

It is the right first step. Move a suite over, get it green, and add Couchbase
when you want more than one replica. The Helm chart refuses `replicaCount > 1`
without a store rather than letting stubs register on one pod and vanish on the
next request.

---

## Step 3 — Move the mappings in

Once the unsupported mappings are dealt with, the same import call lands them:

```sh
curl -s -o /dev/null -w '%{http_code}\n' \
     -X POST http://localhost:18411/__admin/mappings/import \
     -H 'Content-Type: application/json' --data-binary @import.json
```

```
200
```

### Give every mapping a stable id first

This is the one trap worth calling out. `duplicatePolicy` and
`deleteAllNotInImport` both identify a mapping by its `id` (`uuid` is an alias).
A mapping with no `id` gets a fresh server-generated UUID on every import, so
importing the same file twice creates two of everything:

```sh
I=http://localhost:18411/__admin/mappings/import
curl -s -o /dev/null -X POST $I -H 'Content-Type: application/json' --data-binary @import.json
curl -s -o /dev/null -X POST $I -H 'Content-Type: application/json' --data-binary @import.json
curl -s http://localhost:18411/__admin/mappings | jq .meta
```

```json
{ "total": 6 }
```

Three mappings, imported twice, six stubs — and the duplicates all match the
same URLs, so which one answers is decided by insertion order rather than by
anything you wrote. Put an `id` on each mapping and the same file imported three
times leaves three stubs:

```json
{ "total": 3 }
```

This matters more here than under WireMock for a reason that has nothing to do
with the API: a WireMock process is usually restarted between runs and a shared
mockulus deployment is not, so the duplicates accumulate. Add stable ids to the
mapping files themselves. They must be canonical 36-character UUIDs — an
uppercase one is accepted and stored lower-cased, a dashless or base64 spelling
is refused with `id must be a canonical 36-character UUID`.

### `duplicatePolicy`

`importOptions.duplicatePolicy` decides what happens to an id that already
exists.

- `OVERWRITE` (the default) replaces the stub in place. It preserves the stub's
  insertion sequence, so editing a mapping does not change what it wins against.
- `IGNORE` leaves the stored stub untouched. Re-importing an existing id whose
  response had been changed to `{"status": 500, "body": "CHANGED"}` under
  `IGNORE` leaves the original `{"id":42,"tier":"gold"}` response serving.

One divergence to know: if you send an `importOptions` object **without**
`duplicatePolicy`, mockulus applies the documented default `OVERWRITE`, while
WireMock treats the absent policy as `IGNORE`. Omitting `importOptions`
entirely gives `OVERWRITE` on both. If your import payload has a partially
filled `importOptions`, spell the policy out (SPEC §5.5 deviation 40).

### `deleteAllNotInImport`

`importOptions.deleteAllNotInImport: true` removes every stub whose id is not in
the payload. It turns the import into a declarative sync: the deployment ends up
holding exactly what the file describes, which is what you want from a CI job
that owns the mock deployment.

It is also the sharpest edge in this document, because "every stub not in the
payload" includes stubs belonging to other suites. Use it only where the
deployment is yours.

The server logs what each import did:

```
level=INFO msg="mappings imported" imported=1 ignored=0 removed=1 policy=OVERWRITE
```

### `__files`

`bodyFileName` works, but the files live in the store rather than on a disk next
to the process, so they have to be uploaded:

```sh
for f in __files/*; do
  curl -s -o /dev/null -w "%{http_code} $(basename "$f")\n" \
       -X PUT "http://localhost:18411/__admin/files/$(basename "$f")" \
       --data-binary "@$f"
done
```

```
201 customer-42.json
```

`GET /__admin/files` lists them. Registering a stub before its file exists is
legal, as it is in WireMock; the reference resolves when the file appears. Until
then the stub serves a loud failure rather than an empty body:

```
HTTP/1.1 500 Internal Server Error
Content-Type: application/json

{"errors":[{"code":1022,"title":"Body file not found",
            "detail":"the stub's bodyFileName never-uploaded.json has no corresponding file"}]}
```

If you forget the upload step, that is the message you will see.

### `persistent` and the TTL

WireMock keeps a non-persistent stub until the process restarts. There is no
process restart to rely on in a long-lived cluster, so a stub with
`persistent: false` (the default) is stored with a TTL — `ephemeral_stub_ttl`,
default `24h` — and expires on its own. `persistent: true` stores it without a
TTL, and it then survives `mappings/reset` and `/__admin/reset`.

Use `persistent: true` for shared-environment mocks that should outlive any
individual test run, and leave test-registered stubs at the default so CI churn
cannot accumulate forever.

`POST /__admin/mappings/save` exists and returns 200, but it does something
different from WireMock's: instead of writing the in-memory stubs out to a
filesystem — there is no per-node filesystem to write to — it marks every
current stub `persistent: true`, which is the same intent expressed in this
data model. After a `save`, the mappings echo back carrying `"persistent": true`.

---

## Step 4 — Point your clients at it

WireMock serves `/__admin` on the same port as the mock traffic, and every
client library assumes it. That is why `admin_on_mock_port` defaults to `true`:
a client configured with one host and one port keeps working. Every `/__admin`
call in this guide went to port 18411 — the **mock** port — which is the
demonstration.

There are two listeners:

| Listener | Default | Serves |
|---|---|---|
| Mock | `8080` | Mock traffic, and `/__admin/**` unless `admin_on_mock_port: false` |
| Admin / ops | `9090` | `/__admin/**`, `/healthz`, `/readyz`, `/metrics`, `/debug/pprof/**` |

The second listener exists so that an ingress can expose 8080 without exposing
health, metrics and profiles. When the mock port is reachable from outside the
namespace, the hardened posture is to turn the admin API off there:

```sh
MOCKULUS_ADMIN_ON_MOCK_PORT=false ./bin/mockulus
```

Know what that does to a misconfigured client. `/__admin` stops being a special
prefix on the mock port, so an admin call against it falls through to the match
engine and comes back as an ordinary unmatched request — on an empty instance:

```
HTTP/1.1 404 Not Found
Content-Type: text/plain;charset=UTF-8

No response could be served as there are no stub mappings in this mockulus instance.
```

A client library will report that as an unexpected response rather than as
"admin is on the other port", and on a loaded deployment the body is the even
less informative `Request was not matched`. Make the change deliberately and
repoint the clients at 9090 in the same step.

If you set `admin_auth_token`, the whole `/__admin` mux requires
`Authorization: Token <t>` on both listeners:

```json
{"errors":[{"code":10,"title":"Malformed request",
            "detail":"admin API requires a valid Authorization token"}]}
```

So does `/debug/pprof/**` — a heap profile is a copy of every stub body the
process is holding, which is the thing the token exists to protect. `/healthz`,
`/readyz` and `/metrics` stay open whatever the token setting, because the
kubelet and Prometheus cannot present one and none of the three carries stub
content:

```
readyz 200
metrics 200
pprof 401
```

---

## Step 5 — Turn on what is off by default

Two WireMock behaviours are off here, and both will look like bugs until you
turn them on.

### The journal, for `verify()`

WireMock journals every request by default. A deployment taking tens of
thousands of requests a second would then be writing tens of thousands of
journal documents a second, which recreates the collapse this project exists to
avoid — so `journal_enabled` defaults to `false`, and every journal-backed
endpoint says so:

```
HTTP/1.1 500 Internal Server Error
Content-Type: application/json

{"errors":[{"code":1010,"title":"Journal disabled",
            "detail":"the request journal is disabled; set journal_enabled to record and verify requests"}]}
```

If your suite calls `verify()`, `findAll()`, or reads `/__admin/requests`, the
deployment serving it needs `MOCKULUS_JOURNAL_ENABLED=true`. With it on, the
familiar calls work — here a second after one `POST /api/orders`, so the flusher
has run:

```sh
curl -s -X POST http://localhost:18411/__admin/requests/count \
  -H 'Content-Type: application/json' \
  -d '{"method":"POST","urlPath":"/api/orders",
       "bodyPatterns":[{"matchesJsonPath":{"expression":"$.items[0].sku","equalTo":"WIDGET-1"}}]}'
```

```json
{"count":1,"requestJournalDisabled":false}
```

The practical split is one deployment per purpose: journal on for the functional
suites that verify, journal off for the load tests that do not. Two bounds apply
when it is on, and functional suites sit well inside both: criteria queries scan
the newest `journal_query_scan_limit` (10,000) entries, and stored request
bodies are capped at `journal_max_body` (64 KiB).

### Near-miss diagnostics, for debugging

WireMock computes near-misses on every unmatched request and prints them in the
404 body. Mockulus does not, by default, because scoring every unmatched request
costs CPU on the hot path for output nobody reads in production. The default 404
is the fast one:

```
HTTP/1.1 404 Not Found
Content-Type: text/plain;charset=UTF-8

Request was not matched
```

`MOCKULUS_DIAGNOSTICS_ON_UNMATCHED=true` restores the detail. The status,
`Content-Type` and first line stay identical — the diagnostics are appended, not
substituted:

```
Request was not matched
Closest stubs:

  aaaaaaaa-0000-4000-8000-000000000000
    url: expected /api/customers/42, got /api/customers/43

  aaaaaaaa-0000-4000-8000-000000000002
    url: expected /api/orders/o-1001, got /api/customers/43

  aaaaaaaa-0000-4000-8000-000000000001
    method: expected POST, got GET
    url: expected /api/orders, got /api/customers/43
    body: expected matchesJsonPath "$.items[0].sku", got <absent>
```

Turn it on in a dev or CI deployment; leave it off where throughput matters.
The ordering of the list is mockulus's own and is not WireMock's — diagnostic
text is a debugging aid, and no matching decision depends on it.

The **near-miss endpoints** work regardless of the flag, because they compute on
demand on the admin path where the cost is affordable:

```sh
curl -s -X POST http://localhost:18411/__admin/near-misses/request \
  -H 'Content-Type: application/json' \
  -d '{"method":"GET","url":"/api/customers/43"}' \
| jq '.nearMisses[0] | {distance: .matchResult.distance, stub: .stubMapping.request}'
```

```json
{
  "distance": 0.029411764705882353,
  "stub": {
    "method": "GET",
    "urlPath": "/api/customers/42"
  }
}
```

Lower is closer, and a stub that would have matched scores `0`. So a
production-shaped deployment can still be debugged without a restart or a
config change.

---

## Step 6 — The deviations most likely to change your suite

Mockulus answers differently from WireMock in 47 catalogued places, all of them
listed with their rationale in [Deviations from WireMock](deviations.md). Most
will never touch you. These are the ones that do.

### Mappings WireMock accepted and mockulus refuses

Each of these is a document WireMock takes and then quietly does something other
than what it says. Refusing is the deliberate choice; the cost is that a
mappings file using the spelling registers there and not here. All are caught at
registration, so step 1 finds them, and all but the last carry code `10`.

| What you wrote | Answer here |
|---|---|
| `"response": {"body": "hi", "jsonBody": {…}}` | `exactly one body form may be set, found body, jsonBody` — WireMock keeps `body` and discards the rest |
| `{"absent": false}` on a header or query criterion | `"absent": false is not a matcher; omit the criterion instead` — WireMock stores it as `absent: true`, inverting your intent. Write `{"not": {"absent": true}}` for "must be present" |
| `"request": {"url": "/x", "urlPath": "/x"}` | `only one URL criterion may be given, found url, urlPath` — WireMock picks one by a fixed field precedence and silently drops the others from its echo |
| `"status": "200"`, `"fixedDelayMilliseconds": 12.5` | `status must be an integer` / `fixedDelayMilliseconds must be an integer` — WireMock coerces and serves a value you did not write |
| `id` and `uuid` set to different values | `id and uuid are aliases and must not disagree` — WireMock lets the last spelling in the document win, so a later `PUT` or `DELETE` on the other id hits nothing |
| An unrecognised `${json-unit.*}` placeholder | `unknown json-unit placeholder "${json-unit.any-uuid}"` — WireMock compares it as literal text, so the stub never matches and never says why. The documented set (`ignore`, `ignore-element`, `any-string`, `any-number`, `any-boolean`, `regex`) works as it does there |
| A custom `transformers` entry | code `1004`: `unknown transformer "my-transformer"; only "response-template" is supported` |

An unknown Handlebars helper is code `1002`, and the message lists what is
available:

```
unknown helper "myHelper"; mockulus supports base64, concat, default, join, jsonPath,
lookup, lower, lowercase, math, now, number, pickRandom, randomDecimal, randomInt,
randomValue, range, replace, size, split, substring, trim, upper, uppercase, urlEncode
```

`xPath`, `soapXPath`, `formatXml`, `jwt`, `secret`, `systemValue`, `hostname`
and `file` are not in that list. The last four are excluded on purpose: a
template must not be able to read the environment, the filesystem or the
network.

### Timing

- **Journal visibility** (deviation 10). Entries are visible within the flush
  interval plus index lag. Use your client's polling `verify()`.
- **Stub propagation** (deviation 11). Bounded by `sync_interval` across
  replicas; immediate on the replica that took the write.
- **A naturally expired ephemeral stub** (deviation 17) can keep matching on a
  pod that already holds it for up to `resync_interval` (`5m`), because a TTL
  expiry does not bump the epoch. Explicit deletes and resets propagate within
  `sync_interval`.

### Matching outcomes

- **WireMock's JSON parser is more permissive** (deviation 35). A request body
  with trailing content after a complete document, single-quoted keys, or `/* */`
  comments parses there and does not here, so `equalToJson` and
  `matchesJsonPath` match there and do not here. The same strictness applies to
  the `equalToJson` operand, which is refused at registration.
- **A repeated header or query parameter is plain any-of** (deviation 29). The
  criterion holds when any value satisfies it. WireMock instead picks the value
  at minimum edit distance and matches only that one, so a key carrying a near
  miss alongside an exact match can answer differently there.
- **`matchesJsonPath` in its nested form does not distribute over arrays**
  (deviation 42). `{"expression": "$.tags", "equalTo": "red"}` does not match
  `{"tags": ["red"]}`. The bare form agrees with WireMock.
- **`caseInsensitive` folds by Unicode simple case folding** (deviation 43),
  where Java folds per UTF-16 code unit. They disagree on the Turkish dotted and
  dotless I in one direction and on supplementary-plane pairs in the other.
- **`equalToDateTime` against a bare date matches that whole day** (deviation
  51). WireMock reads a date-only value as midnight, so
  `equalToDateTime: "2021-06-14"` matches only `00:00:00` there and excludes
  every other moment of the day it names. Mockulus matches any instant that day.
  The widening is confined to equality — `before` and `after` keep midnight —
  so mockulus only ever matches *more*, and a suite that passes on WireMock
  cannot fail here because of it.

### `matchesJsonSchema`, and the draft that decides `format`

Schema matching works. Two things about it are worth knowing before you rely on
one, and both are WireMock's behaviour reproduced rather than choices of ours.

**The draft decides whether `format` does anything.** `schemaVersion` takes
exactly `V4`, `V6`, `V7`, `V201909` and `V202012`, and defaults to `V202012`.
Under 2019-09 and 2020-12 `format` is an *annotation* — the JSON Schema spec
moved it into a vocabulary that is off by default — so out of the box this
matches:

```jsonc
{ "matchesJsonSchema": { "type": "string", "format": "email" } }
// request body: "not-an-email"   → MATCHES, because the default draft ignores format
```

If you want `format` enforced, pin an older draft with `"schemaVersion": "V7"`,
or declare `$schema` in the document itself — a document's own `$schema` wins
over the `schemaVersion` field, in both directions.

**`$ref` resolves inside the document only.** `$defs`, `definitions`, JSON
pointers, `$anchor` and `$id` all work. A reference to a remote URL is refused
when you register the stub (deviation 56). WireMock accepts it, but it never
fetches it either — the difference is that WireMock's stub then silently matches
nothing, with no error anywhere, while mockulus tells you at registration.

The other refusals in that deviation are all schemas that could not have worked
on WireMock either: a `type` that names no type, a `$ref` to a location that is
not there, and a bare value like `42` — which on WireMock registers and then
matches *every* request.

One difference runs the other way. `matchesJsonSchema` here validates the parsed
JSON document, so a body that is not JSON is a non-match (deviation 55). WireMock
falls back to validating the raw request text as a JSON string, which makes it
match *more* — and makes it self-contradictory for scalar bodies, where a schema
and its own negation can both match. If your stubs validate object or array
bodies, which is what schemas are normally written for, the two agree exactly.

### The date-time matchers, and the rule that is not obvious

`before`, `after` and `equalToDateTime` work, and they follow WireMock exactly on
everything below except where a deviation is named. One rule is worth knowing
before you write one, because it is the opposite of what most people assume.

**The expected value's type decides what is being compared.** If the expected
value carries a zone, the two sides are compared as *instants*: the request's
offset is honoured, and a request value with no offset is read in the pod's own
timezone. If the expected value has no zone, the two are compared as
*wall-clock readings*, and the request's offset is **discarded rather than
converted**. So:

```jsonc
// expected carries Z → instants. 12:00+03:00 is 09:00Z, so this matches.
{ "after": "2021-06-14T12:00:00+03:00" }   // request: 2021-06-14T10:00:00Z  → match

// expected carries no zone → wall clock. The request's instant is 10:00Z,
// two hours EARLIER, and it still counts as after, because 13:00 > 12:00.
{ "after": "2021-06-14T12:00:00" }         // request: 2021-06-14T13:00:00+03:00 → match
```

That is WireMock's behaviour, reproduced deliberately. The practical advice is to
write your expected values **with an offset** unless you specifically want
wall-clock comparison, because instants are what almost everyone means.

Three more things that catch people, all WireMock's behaviour and all reproduced:

- `before` and `after` are **strict**. A request value exactly equal to the
  expected satisfies neither. There is no `beforeOrEqualTo`; write
  `{"or": [{"equalToDateTime": …}, {"before": …}]}`.
- `actualFormat` **replaces** ISO parsing rather than adding to it. Once you set
  it, an ISO-8601 request value stops matching.
- `unix` means epoch **seconds** and `epoch` means epoch **milliseconds**. They
  are not synonyms, and mixing them up is a factor-of-1000 error.

Where mockulus refuses what WireMock accepts, it is always a criterion that could
not have worked there either (deviations 49, 50, 52, 53): an operand WireMock
takes and can never match — a `+0300` offset without its colon, `now+2days`
without spaces, a bare epoch number — a truncation parameter in a position where
WireMock silently ignores it, or an `actualFormat` on a matcher that has no date
to format. Each is a 422 naming the field, so the assessment step above finds
them all at once.

### Responses on the wire

- **A response with `statusMessage` closes the connection** (deviation 7). Go's
  `net/http` cannot set a reason phrase, so such a response is written over a
  hijacked connection, and nothing keeps serving that connection afterwards:

  ```
  < HTTP/1.1 418 I am a teapot
  < Connection: close
  ```

  A stub that does not set `statusMessage` is untouched. Note also that the
  reason phrase for a response that does not set one is Go's canonical text
  (`500` → `Internal Server Error`), not Jetty's (`500` → `Server Error`) —
  deviation 30.
- **Request bodies are capped at 10 MiB** (deviation 6), answering `413` beyond
  it where WireMock is unbounded. `max_body_bytes: 0` removes the cap.
- **The unmatched 404 names mockulus**, not WireMock (deviation 18). Shape,
  status and `Content-Type` are identical. A suite asserting on that exact
  string needs updating.

### The echoed mapping document

If your tests assert on what `GET /__admin/mappings` returns — as opposed to
what the stub serves — check these. Serving agrees in every case; only the
document differs.

- Mockulus echoes the document you registered. WireMock fills in defaults on the
  way out: an absent `response` becomes `{"status": 200}`, an absent `request`
  becomes `{"method": "ANY"}` (deviation 41).
- A response header registered as a one-element array stays an array here;
  WireMock collapses it to a bare string (deviation 44).
- `GET /__admin/scenarios` returns `id`, `name`, `state` and `possibleStates`.
  WireMock additionally embeds every member stub under `mappings` (deviation 32).
- `remove-by-metadata` returns the removed mappings; WireMock answers `{}`.

---

## Step 7 — When something is unsupported

You will hit a `422` for a feature you need. The answer is deliberately not
"work around it silently".

**The error names the field.** `source.pointer` is a JSON pointer into the
document you sent, and `detail` names the feature and points at
[ROADMAP.md](../ROADMAP.md), where every deferred feature has an entry
recording what it is, why it was deferred, a design sketch consistent with the
v1 architecture, what it depends on, and a size estimate. That entry is the
place to comment on, because it is where the work would start.

**The roadmap's order is a proposal, not a commitment.** Bucket 1 is the set of
compat gaps with known demand — XML and XPath matching and multipart matching;
the date-time and JSON Schema matchers have both since landed. Bucket 2 is the new subsystems: proxy
mode, record and playback, webhooks. The ordering is explicitly reprioritised on
demand signal, so the useful thing to do with a gap is report it rather than
route around it. A v1.x feature landing can only ever turn a `422` into a
supported field; it can never change the behaviour of a stub that registers
today.

**Refusals are counted.** Every admin response is counted by endpoint group and
HTTP status:

```sh
curl -s http://localhost:18511/metrics | grep '^mockulus_admin_requests_total'
```

```
mockulus_admin_requests_total{code="200",endpoint_group="mappings/import"} 1
mockulus_admin_requests_total{code="200",endpoint_group="requests/count"} 1
mockulus_admin_requests_total{code="200",endpoint_group="requests/unmatched"} 1
mockulus_admin_requests_total{code="422",endpoint_group="mappings"} 7
```

That counter is the volume signal: a deployment where `code="422"` is climbing
is a deployment whose users are hitting the edge of the subset, and it is worth
a look before anyone files anything. What the metric deliberately does not carry
is the catalog code or the field name — per-stub and per-field labels would mint
a series per stub in a ten-thousand-stub deployment. The attribution lives in the
`422` body, which is the artifact your import script already has. Keep the
pointers your assessment produced; they are the evidence a roadmap entry needs.

---

## Checklist

1. Collate `mappings/` into one import payload and `POST /__admin/mappings/import`
   against a throwaway instance. Fix or triage every pointer in the `422`.
2. Add stable canonical-UUID `id`s to the mapping files.
3. Upload `__files` through `PUT /__admin/files/{name}`.
4. Start single-replica with the in-memory store. Point the existing clients at
   the mock port; nothing else about them changes.
5. Turn on `journal_enabled` if the suite verifies, and
   `diagnostics_on_unmatched` while you are debugging.
6. Replace any global `reset()` with metadata-scoped cleanup before the
   deployment is shared.
7. Add Couchbase and raise the replica count when the suite is green —
   [Operating mockulus](operations.md) covers the topologies and the probes.
