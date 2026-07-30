# Compatibility matrix

Mockulus implements a **subset** of WireMock 3.x. This document is that subset,
behavior by behavior: which parts of the admin API and the stub format work,
which work differently, which are refused — and what in the test suite proves
each answer.

Anything outside the subset **fails loudly**. An unsupported stub field is
rejected at registration: `422`, an error body whose `source.pointer` names the
field, and a `detail` pointing at ROADMAP.md. An unsupported admin path is `404`
with code `1001`. Nothing is accepted and quietly ignored, so a mapping
that registers is a mapping that matches. That is the one property to carry into
a migration: you find out about the gap when you load your stubs, not when a
test fails at three in the morning.

One `422` reports every problem it found, so a mappings file is fixed in one
round rather than one field per attempt:

```console
$ curl -sS -X POST localhost:9090/__admin/mappings -H 'Content-Type: application/json' -d '{
  "request": {
    "method": "GET",
    "urlPath": "/orders",
    "bodyPatterns": [{"matchesXPath": "//order"}]
  },
  "response": {"status": 200, "proxyBaseUrl": "http://real.example.com"}
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
    },
    {
      "code": 1000,
      "source": {
        "pointer": "/response/proxyBaseUrl"
      },
      "title": "Unsupported feature",
      "detail": "proxyBaseUrl (proxy mode) is not supported in mockulus v1 — see ROADMAP.md"
    }
  ]
}
```

If you are evaluating a move from WireMock, start with
[what is refused](#what-is-refused) and then read
[the deviations](#deliberate-deviations). Together they are the afternoon.

## How this document is made

It is generated. The three files it is derived from are the same three the E2E
regression gate is derived from:

| Source | What it contributes |
|---|---|
| [`SPEC.md`](../SPEC.md) | The rows themselves — every endpoint, field, matcher, helper, deviation, error code, config key and metric, with the spec's own description of each |
| [`test/e2e/catalog/`](../test/e2e/catalog) | The behavior identifier for each row, and the assertion a case must make to count as covering it |
| [`test/e2e/corpus/`](../test/e2e/corpus), [`test/e2e/gotests/`](../test/e2e/gotests) | The cases, and which behavior each one binds |

Regenerate after any change to those:

```sh
make compat-docs
```

The generator rewrites only the region between the two `GENERATED MATRIX`
comments below; everything above them is written by hand and is preserved. It
fails rather than guesses: a catalog entry whose spec row it cannot find, or a
spec block that has been restructured, exits non-zero rather than quietly
emitting a thinner document. `make compat-docs-check` reports whether the
committed file still matches its sources without rewriting it.

This is not documentation discipline, it is the only way a matrix this size
stays true. A hand-maintained one is a document that was accurate on the day
somebody last had the patience for it.

## The two verification tags

Every case in the corpus declares how its expectations were arrived at, and the
matrix reports that per behavior. The distinction is the whole basis of the
compatibility claim.

**`verified`** — at least one case behind the row had its expectations
re-derived from the pinned WireMock image by running the same operations against
it. Topology T5 of the gate replays those cases against both servers and diffs
status, headers and body, with JSON compared structurally under subset semantics
(SPEC §5.6). A `verified` row is a statement about WireMock, checked against
WireMock — not against someone's reading of its documentation.

**`n/a`** — there is nothing to diff against. Either the behavior is mockulus's
own (a degraded mode, a Prometheus collector, propagation across replicas), or
it is a deviation whose entire content is that the two servers differ. Those
expectations come from the spec, and the case pins the spec.

Neither tag means "untested". Every case of both kinds runs on every pull
request and must pass; what the merge pipeline adds for the `verified` ones is
the side-by-side against WireMock itself (SPEC §19.5). The tag says what a case
is evidence *of*, not how often it runs.

## Reading the tables

- **v1** — ✅ supported · 🔶 supported with a documented deviation · ❌ refused,
  with the status it is refused with · ⏳ specified and catalogued, but the
  milestone that implements it has not landed. ○ in the Evidence column marks a
  row with no distinct observable of its own; the reason is printed under the
  table.
- **Evidence** — how many E2E cases bind this behavior, and the verification tag
  they carry. `12 · verified` means twelve cases, at least one of them diffed
  against pinned WireMock. `(2 Go-native)` counts those of them written in Go
  against a raw socket or the process, for behavior an HTTP client cannot
  observe — a fault that closes the connection mid-body, an h2c upgrade, a
  drain window.
- **Behavior** — the catalog identifier. Grep it in `test/e2e/catalog/` for the
  assertion a case must make to count, and in `test/e2e/corpus/` for the cases
  that make it.
- **Notes** — the spec's own words for the row, unedited.

Every behavior listed here must be bound by a passing case before the gate goes
green, and a case must assert something specific to the behavior it claims —
a bare `status: 200` cannot claim to cover an error code (SPEC §19.2). That is
what makes the Evidence column worth reading.

<!-- BEGIN GENERATED MATRIX -->

## At a glance

| | Count |
|---|---:|
| WireMock surface — supported | 73 |
| WireMock surface — supported with a documented deviation | 8 |
| WireMock surface — not supported (422 or 404, with a ROADMAP pointer) | 8 |
| Deliberate deviations from WireMock | 54 |
| Catalogued behaviors in total | 226 |
| … of those, with no distinct observable of their own (reviewed exemptions) | 3 |
| Behaviors stated in prose rather than a table | 7 |
| E2E corpus cases | 572 |
| … `wm: verified` — expectations re-derived from `wiremock/wiremock:3.13.2` | 380 |
| … `wm: n/a` — expectations from the spec | 192 |
| Go-native cases (raw socket, process lifecycle) | 27 |

Milestone cursor `M7`; oracle pinned at `wiremock/wiremock:3.13.2`. SPEC §5.6 sets ≥300 differentially
verified cases as a v1.0 release criterion.

Every catalogued behavior is bound by at least one case. The E2E gate fails when that stops being true (SPEC §19.2).

## The WireMock surface

### What is refused

8 rows of the surface below are not implemented, and every one of them is refused rather than ignored. A mapping carrying one of the stub fields never registers, so a suite that depends on it fails when it loads its mappings — not later, and not quietly; an admin path that is not implemented is 404 with code 1001 rather than a plausible-looking empty answer. [ROADMAP.md](../ROADMAP.md) tracks each with a design sketch and a size.

| Feature | Answer | Behavior | Note |
|---|---|---|---|
| `/__admin/recordings/**`, `/__admin/proxy/**`, `/__admin/certificates/**`, `/__admin/mappings/unmatched`* | ❌ | `B-ADMIN-RECORDINGS-PROXY-CERTIFICATES-MAPPINGS-UNMATCHED` | 404 + error body `{"errors":[{code:1001,...}]}` linking ROADMAP.md |
| `postServeActions` | ❌ 422 | `B-STUB-POSTSERVEACTIONS` | Webhooks deferred |
| `multipartPatterns` | ❌ 422 | `B-REQ-MULTIPARTPATTERNS` | Roadmap |
| `customMatcher`, `hasExactly`/`includes` multi-value ops | ❌ 422 | `B-REQ-CUSTOMMATCHER-HASEXACTLY-INCLUDES-MULTI-VALUE-OPS` | Roadmap |
| `matchesJsonSchema` | ❌ 422 | `B-MATCH-MATCHESJSONSCHEMA` | Roadmap |
| `equalToXml`, `matchesXPath` | ❌ 422 | `B-MATCH-EQUALTOXML-MATCHESXPATH` | Roadmap |
| `proxyBaseUrl`, `additionalProxyRequestHeaders`, `proxyUrlPrefixToRemove` | ❌ 422 | `B-RESP-PROXYBASEURL-ADDITIONALPROXYREQUESTHEADERS-PROXYURLPREFIXTOR` | Roadmap (proxy mode) |
| `fromConfiguredStub`, `additionalHeaders` (proxy-related) | ❌ 422 | `B-RESP-FROMCONFIGUREDSTUB-ADDITIONALHEADERS-PROXY-RELATED` | — |

### Admin API endpoints

[SPEC §5.1](../SPEC.md#51-admin-api-endpoint-matrix) · 33 behaviors

Every `/__admin` path mockulus answers. Anything not listed — and every path under `/__admin/recordings`, `/__admin/proxy` and `/__admin/certificates` — is 404 with an error body carrying code 1001 and a link to ROADMAP.md.

| Method & path | v1 | Evidence | Behavior | Notes |
|---|---|---|---|---|
| `POST /__admin/mappings` | ✅ | 15 · verified | `B-ADMIN-MAPPINGS-POST` | Create. Returns 201 + created stub (server assigns `id`/`uuid` if absent). An `id` that already exists is rejected 422 code 109 — create is not an upsert |
| `GET /__admin/mappings` | ✅ | 4 · verified | `B-ADMIN-MAPPINGS-GET` | List, WM envelope `{"mappings":[...],"meta":{"total":n}}`; `limit`/`offset` params |
| `DELETE /__admin/mappings` | ✅ | 1 · n/a | `B-ADMIN-MAPPINGS-DELETE` | Delete all (persistent and not) |
| `POST /__admin/mappings/reset` | ✅ | 1 · n/a | `B-ADMIN-MAPPINGS-RESET-POST` | Remove non-persistent stubs; reload snapshot |
| `GET /__admin/mappings/{id}` | ✅ | 13 · verified | `B-ADMIN-MAPPINGS-ID-GET` | 404 WM-style if absent |
| `PUT /__admin/mappings/{id}` | ✅ | 13 · verified | `B-ADMIN-MAPPINGS-ID-PUT` | Full replace of an **existing** stub; 404 with an empty body if the id is unknown, and the existence check precedes body parsing (so an invalid body against an unknown id is 404, not 422). A body `id` disagreeing with the path is ignored — the path wins. Preserves the stub's `seq` (§7.3); 422 on unsupported fields |
| `DELETE /__admin/mappings/{id}` | ✅ | 12 · verified | `B-ADMIN-MAPPINGS-ID-DELETE` | — |
| `POST /__admin/mappings/save` | 🔶 | 2 · n/a | `B-ADMIN-MAPPINGS-SAVE-POST` | WM: persist in-memory stubs to backing store. Ours: set `persistent=true` on all current stubs (removes TTL). Documented deviation |
| `POST /__admin/mappings/import` | ✅ | 20 · verified | `B-ADMIN-MAPPINGS-IMPORT-POST` | `{"mappings":[...], "importOptions":{...}}`; `duplicatePolicy: OVERWRITE\|IGNORE` (default `OVERWRITE`; `OVERWRITE` replaces in place preserving `seq`, `IGNORE` leaves the existing stub untouched), `deleteAllNotInImport` removes every stub whose id is not in the payload. 200 on success |
| `POST /__admin/mappings/find-by-metadata` | ✅ | 5 · n/a | `B-ADMIN-MAPPINGS-FIND-BY-METADATA-POST` | Body = one content matcher applied to stub `metadata` (reuses §5.3 matchers) |
| `POST /__admin/mappings/remove-by-metadata` | ✅ | 4 · n/a | `B-ADMIN-MAPPINGS-REMOVE-BY-METADATA-POST` | Same matcher. WM answers `{}` with no detail; ours additionally returns the removed mappings under the standard list envelope — a catalogued extension, not a diff (§5.6) |
| `GET /__admin/requests` (+`limit`,`since`) | ✅ | 1 · n/a | `B-ADMIN-REQUESTS-LIMIT-SINCE-GET` | Journal-backed. Journal disabled → WM's journal-disabled error **[DH]** (provisionally 500, code in Appendix B) |
| `DELETE /__admin/requests` | ✅ | 2 · n/a | `B-ADMIN-REQUESTS-DELETE` | Clear journal (this deployment's journal collection) |
| `GET /__admin/requests/{id}` | ✅ | 2 · n/a (1 Go-native) | `B-ADMIN-REQUESTS-ID-GET` | — |
| `DELETE /__admin/requests/{id}` | ✅ | 2 · n/a (1 Go-native) | `B-ADMIN-REQUESTS-ID-DELETE` | — |
| `POST /__admin/requests/reset` | 🔶 | 1 · n/a | `B-ADMIN-REQUESTS-RESET-POST` | Deprecated WM alias of DELETE — implement as alias |
| `POST /__admin/requests/count` | ✅ | 5 · n/a | `B-ADMIN-REQUESTS-COUNT-POST` | Body = request criteria (same model as stub `request`); `{"count":n}` |
| `POST /__admin/requests/find` | ✅ | 1 · n/a | `B-ADMIN-REQUESTS-FIND-POST` | `{"requests":[...]}` |
| `POST /__admin/requests/remove` | ✅ | 2 · n/a | `B-ADMIN-REQUESTS-REMOVE-POST` | Remove matching entries |
| `GET /__admin/requests/unmatched` | ✅ | 3 · n/a | `B-ADMIN-REQUESTS-UNMATCHED-GET` | Journal entries with `matched=false` |
| `GET /__admin/requests/unmatched/near-misses` | ✅ | 4 · n/a | `B-ADMIN-REQUESTS-UNMATCHED-NEAR-MISSES-GET` | Computed on demand (admin path — near-miss cost acceptable here, §5.4) |
| `POST /__admin/near-misses/request` | ✅ | 4 · n/a | `B-ADMIN-NEAR-MISSES-REQUEST-POST` | On-demand computation against current snapshot |
| `POST /__admin/near-misses/request-pattern` | ✅ | 5 · n/a | `B-ADMIN-NEAR-MISSES-REQUEST-PATTERN-POST` | — |
| `GET /__admin/scenarios` | ✅ | 3 · n/a | `B-ADMIN-SCENARIOS-GET` | `{"scenarios":[{"id","name","state","possibleStates":[...]}]}`; possibleStates derived from snapshot **[DH]** shape |
| `POST /__admin/scenarios/reset` | ✅ | 3 · n/a | `B-ADMIN-SCENARIOS-RESET-POST` | All scenarios → `Started` |
| `PUT /__admin/scenarios/{name}/state` | ✅ | 4 · verified | `B-ADMIN-SCENARIOS-NAME-STATE-PUT` | `{"state":"..."}`; 404 unknown scenario; 400 unknown state **[DH]** |
| `POST /__admin/reset` | ✅ | 1 · n/a | `B-ADMIN-RESET-POST` | mappings/reset + journal clear + scenarios reset |
| `GET /__admin/settings`, `POST /__admin/settings` | 🔶 | 2 · n/a | `B-ADMIN-SETTINGS-POST-SETTINGS-GET` | Subset: `fixedDelay`, `delayDistribution` (global response delay). Persisted as the `meta::settings` doc and epoch-bumped ⇒ cluster-wide, restart-safe, compiled into the snapshot (§7.2). Unknown settings keys → 422. WM extended settings ❌ |
| `GET /__admin/health` | ✅ | 1 · verified | `B-ADMIN-HEALTH-GET` | WM 3.2+ shape `{"status":"healthy",...}` + our detail (store status, stub count) |
| `GET /__admin/version` | ✅ | 1 · verified | `B-ADMIN-VERSION-GET` | `{"version":"<mockulus ver>","guessedWireMockVersion":"3.x-subset"}` — additive fields allowed |
| `GET/PUT/DELETE /__admin/files/{name}`, `GET /__admin/files` | ✅ | 6 · n/a | `B-ADMIN-GET-PUT-DELETE-FILES-NAME-GET-FILES` | Backed by `files` collection (§7.3); powers `bodyFileName` |
| `POST /__admin/shutdown` | 🔶 | 1 · n/a | `B-ADMIN-SHUTDOWN-POST` | Disabled by default (K8s owns lifecycle); flag `admin_shutdown_enabled` |
| `/__admin/recordings/**`, `/__admin/proxy/**`, `/__admin/certificates/**`, `/__admin/mappings/unmatched`* | ❌ | 1 · n/a | `B-ADMIN-RECORDINGS-PROXY-CERTIFICATES-MAPPINGS-UNMATCHED` | 404 + error body `{"errors":[{code:1001,...}]}` linking ROADMAP.md |

### Stub mapping — top-level fields

[SPEC §5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix) · 9 behaviors

| Field | v1 | Evidence | Behavior | Notes |
|---|---|---|---|---|
| `id`, `uuid` | ✅ | 9 · verified | `B-STUB-ID-UUID` | Aliases; must be a canonical 36-character UUID (parsed case-insensitively, canonicalised to lower case); server-generated UUIDv4 when absent; both echoed |
| `name` | ✅ | 7 · verified | `B-STUB-NAME` | Stored/returned; shown in near-miss output |
| `priority` | ✅ | 28 · verified | `B-STUB-PRIORITY` | Arbitrary signed integer compared numerically — no clamping, no range validation; lower wins. Absent ⇒ effective 5 |
| `persistent` | 🔶 | 1 · n/a | `B-STUB-PERSISTENT` | `true` ⇒ durable doc. `false`/absent ⇒ doc with TTL `ephemeral_stub_ttl` (default 24 h) and removed by `mappings/reset`. WM keeps non-persistent stubs until process restart — TTL is our (documented) equivalent for a long-running cluster |
| `scenarioName`, `requiredScenarioState`, `newScenarioState` | ✅ | 18 · verified | `B-STUB-SCENARIONAME-REQUIREDSCENARIOSTATE-NEWSCENARIOSTATE` | §9 |
| `metadata` | ✅ | 5 · verified | `B-STUB-METADATA` | Arbitrary JSON, stored/returned; searchable via find-by-metadata |
| `postServeActions` | ❌ 422 | 2 · n/a | `B-STUB-POSTSERVEACTIONS` | Webhooks deferred |
| `request` | ✅ | 49 · verified | `B-STUB-REQUEST` | See below |
| `response` | ✅ | 35 · verified | `B-STUB-RESPONSE` | See below |

### Stub mapping — `request`

[SPEC §5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix) · 14 behaviors

| Field | v1 | Evidence | Behavior | Notes |
|---|---|---|---|---|
| `method` | ✅ | 11 · verified | `B-REQ-METHOD` | Specific or `ANY` |
| `url` | ✅ | 14 · verified | `B-REQ-URL` | **Byte-exact** match on path+query as received (WM semantics — query order matters) |
| `urlPattern` | ✅ | 17 · verified | `B-REQ-URLPATTERN` | Regex on full path+query (regex engine: §6.6) |
| `urlPath` | ✅ | 24 · verified | `B-REQ-URLPATH` | Exact path only |
| `urlPathPattern` | ✅ | 15 · verified | `B-REQ-URLPATHPATTERN` | Regex on path only |
| `urlPathTemplate` + `pathParameters` | ✅ | 5 · verified | `B-REQ-URLPATHTEMPLATE-PATHPARAMETERS` | WM 3 templates `/x/{id}`; per-param matchers |
| `queryParameters` | ✅ | 42 · verified | `B-REQ-QUERYPARAMETERS` | Per-param matcher; a repeated param matches if **any** value satisfies the matcher; `absent` supported. `?x=` and bare `?x` are both present-with-empty-string, never absent |
| `headers` | ✅ | 64 · verified | `B-REQ-HEADERS` | Case-insensitive names (both directions); a repeated header matches if **any** value satisfies the matcher; values are case-sensitive unless `caseInsensitive`; `absent` supported |
| `cookies` | ✅ | 5 · verified | `B-REQ-COOKIES` | — |
| `formParameters` | ✅ | 3 · verified | `B-REQ-FORMPARAMETERS` | `application/x-www-form-urlencoded` bodies; parsed lazily |
| `basicAuthCredentials` | ✅ | 1 · verified | `B-REQ-BASICAUTHCREDENTIALS` | Sugar over `Authorization` |
| `bodyPatterns` | ✅ | 41 · verified | `B-REQ-BODYPATTERNS` | All listed patterns must match (AND). Matchers below |
| `multipartPatterns` | ❌ 422 | 1 · n/a | `B-REQ-MULTIPARTPATTERNS` | Roadmap |
| `customMatcher`, `hasExactly`/`includes` multi-value ops | ❌ 422 | 1 · n/a | `B-REQ-CUSTOMMATCHER-HASEXACTLY-INCLUDES-MULTI-VALUE-OPS` | Roadmap |

### Content matchers

[SPEC §5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix) · 11 behaviors

Used in `bodyPatterns`, as the values of `headers`, `queryParameters`, `cookies`, `pathParameters` and `formParameters`, and by verification criteria and `find-by-metadata`.

| Matcher | v1 | Evidence | Behavior | Notes |
|---|---|---|---|---|
| `equalTo` (+`caseInsensitive`) | ✅ | 34 · verified | `B-MATCH-EQUALTO-CASEINSENSITIVE` | — |
| `binaryEqualTo` | ✅ | 5 · verified | `B-MATCH-BINARYEQUALTO` | Base64 operand |
| `contains`, `doesNotContain` | ✅ | 33 · verified | `B-MATCH-CONTAINS-DOESNOTCONTAIN` | — |
| `matches`, `doesNotMatch` | ✅ | 29 · verified | `B-MATCH-MATCHES-DOESNOTMATCH` | Java-regex compat strategy §6.6 |
| `before`, `after`, `equalToDateTime` (+`truncateExpected`, `actualFormat`) | 🔶 | 29 · verified | `B-MATCH-BEFORE-AFTER-EQUALTODATETIME-TRUNCATEEXPECTED-ACTUALFORMAT` | **The expected value's type selects the comparison.** An expected carrying a zone compares *instants* — the actual's offset is honoured and a zoneless actual resolves in the pod's zone; an expected with no zone compares *wall-clock fields* and the actual's offset is discarded rather than converted. So `2021-06-14T12:00:00` reports `2021-06-14T13:00:00+03:00` as later, though that instant is earlier. `before`/`after` are strict; equality is instant-valued and exact to the nanosecond, so `12:13:14Z` equals `12:13:14.000Z`. Operands: ISO-8601 (an offset must carry a colon), a bare date, RFC 1123, and the now-relative forms `now`, `now ±N units`, `±N units` with plural units from `seconds`…`years` (no `weeks`). Modifiers: `truncateExpected`, `truncateActual`, `applyTruncationLast`, `actualFormat` — there is **no** offset parameter, an offset is written into the expected value. `actualFormat` replaces ISO parsing rather than extending it; `unix` is seconds and `epoch` is milliseconds. An unparseable actual is a non-match. Deviations #49–#53 |
| `equalToJson` (+`ignoreArrayOrder`, `ignoreExtraElements`) | ✅ | 40 · verified | `B-MATCH-EQUALTOJSON-IGNOREARRAYORDER-IGNOREEXTRAELEMENTS` | Structural JSON equality; numbers compared by value, so `1` equals `1.0`. `ignoreExtraElements` forgives elements the expected document never accounted for in **arrays as well as objects**: positionally those are the ones past the end, so expected `[1,2]` accepts `[1,2,3]` and still refuses `[3,1,2]`, and an actual array *shorter* than the expected one remains a mismatch. `ignoreArrayOrder` gives up the positions and keeps the count; together they are a subset test — each expected element pairs with a distinct actual element, the unclaimed ones are ignored, and duplicates still have to go round (deviation #25). json-unit placeholders are interpreted as WM does: `ignore`, `ignore-element`, `any-string`, `any-number`, `any-boolean`, and `regex` (full match); in an array a placeholder occupies a slot rather than excusing one. An **unrecognised** placeholder is rejected at registration (deviation #5) |
| `matchesJsonPath` | ✅ | 47 · verified | `B-MATCH-MATCHESJSONPATH` | Bare expression form (presence/non-empty) and nested-matcher form `{"matchesJsonPath":{"expression":"$.x","equalTo":"y"}}`. JSONPath dialect: §6.7 |
| `matchesJsonSchema` | ❌ 422 | 1 · n/a | `B-MATCH-MATCHESJSONSCHEMA` | Roadmap |
| `equalToXml`, `matchesXPath` | ❌ 422 | 1 · n/a | `B-MATCH-EQUALTOXML-MATCHESXPATH` | Roadmap |
| `absent` | ✅ | 32 · verified | `B-MATCH-ABSENT` | Key-level matcher |
| `and`, `or`, `not` | ✅ | 19 · verified | `B-MATCH-AND-OR-NOT` | Combinators over the above |

### Stub mapping — `response`

[SPEC §5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix) · 12 behaviors

| Field | v1 | Evidence | Behavior | Notes |
|---|---|---|---|---|
| `status` | ✅ | 11 · verified | `B-RESP-STATUS` | Default 200 |
| `statusMessage` | 🔶 | 6 · verified | `B-RESP-STATUSMESSAGE` | Sent as the HTTP/1.1 reason phrase. Go's `net/http` cannot choose one, so a stub that sets this is written over a hijacked connection and **the connection closes after the response** (deviation #7); a stub that does not set it is untouched (P2). The phrase is encoded once at registration exactly as WM does: CR and LF each become `?`, a rune outside Latin-1 becomes `?` — so it can neither split the response nor be rejected for something WM accepts. HTTP/2 has no reason phrase, so it is dropped there |
| `headers` | ✅ | 24 · verified | `B-RESP-HEADERS` | Templated when templating active |
| `body` / `jsonBody` / `base64Body` / `bodyFileName` | ✅ | 23 · verified | `B-RESP-BODY-JSONBODY-BASE64BODY-BODYFILENAME` | Exactly one — more than one is rejected 422 (deviation #19). No `Content-Type` is emitted unless the stub sets one. `bodyFileName` resolved from the files store **at snapshot build**, and on the admin write that registers the stub, so the pod that handled the write serves the file's bytes on the very next request rather than an empty body until the next reload (body inlined into memory either way — P1); file missing when resolved ⇒ stub serves 500 code 1022 until the file appears (§6.9) |
| `fixedDelayMilliseconds` | ✅ | 6 · verified | `B-RESP-FIXEDDELAYMILLISECONDS` | §12.4 |
| `delayDistribution` | ✅ | 2 · verified | `B-RESP-DELAYDISTRIBUTION` | `uniform` (lower/upper) and `lognormal` (median/sigma) |
| `chunkedDribbleDelay` | ✅ | 1 · n/a | `B-RESP-CHUNKEDDRIBBLEDELAY` | `numberOfChunks`, `totalDuration` |
| `fault` | ✅ | 5 · n/a (5 Go-native) | `B-RESP-FAULT` | `CONNECTION_RESET_BY_PEER`, `EMPTY_RESPONSE`, `MALFORMED_RESPONSE_CHUNK`, `RANDOM_DATA_THEN_CLOSE` (§12.5) |
| `transformers` | 🔶 | 34 · verified | `B-RESP-TRANSFORMERS` | Only `["response-template"]` recognized; any other transformer name → 422 |
| `transformerParameters` | ✅ | 4 · verified | `B-RESP-TRANSFORMERPARAMETERS` | Exposed to templates as `parameters` |
| `proxyBaseUrl`, `additionalProxyRequestHeaders`, `proxyUrlPrefixToRemove` | ❌ 422 | 1 · n/a | `B-RESP-PROXYBASEURL-ADDITIONALPROXYREQUESTHEADERS-PROXYURLPREFIXTOR` | Roadmap (proxy mode) |
| `fromConfiguredStub`, `additionalHeaders` (proxy-related) | ❌ 422 | 1 · n/a | `B-RESP-FROMCONFIGUREDSTUB-ADDITIONALHEADERS-PROXY-RELATED` | — |

### Response-template helpers

[SPEC §10.3](../SPEC.md#103-helper-allowlist-v1) · 10 behaviors

The allowlist. Any other helper name — `xPath`, `soapXPath`, `formatXml`, `jwt`, `secret`, `systemValue`, `hostname`, `file` — is 422 code 1002 at registration, naming the helper. Environment, file and system access are excluded deliberately (SPEC §17).

| Helper(s) | Evidence | Behavior | Notes |
|---|---|---|---|
| `jsonPath` | 8 · verified | `B-TPL-JSONPATH` | shares the §6.7 engine |
| `now` | 6 · verified | `B-TPL-NOW` | `offset`, `format` (WM tokens + `epoch`/`unix`), `timezone` |
| `randomValue` | 4 · verified | `B-TPL-RANDOMVALUE` | types `ALPHANUMERIC`, `ALPHABETIC`, `NUMERIC`, `UUID`, `HEXADECIMAL`; `length`, `uppercase` |
| `pickRandom` | 2 · n/a | `B-TPL-PICKRANDOM` | — |
| `randomInt`, `randomDecimal` | 2 · n/a | `B-TPL-RANDOMINT-RANDOMDECIMAL` | — |
| `math`, `number` | 5 · verified | `B-TPL-MATH-NUMBER` | arithmetic + number-formatting basics |
| `base64`, `urlEncode` | 4 · verified | `B-TPL-BASE64-URLENCODE` | both with decode option |
| `trim`, `lower`/`lowercase`, `upper`/`uppercase`, `split`, `join`, `concat`, `substring`, `replace`, `size`, `default` | 6 · verified | `B-TPL-TRIM-LOWER-LOWERCASE-UPPER-UPPERCASE-SPLIT-JOIN-CONCAT-SUBST` | string helpers |
| `range` | 3 · verified | `B-TPL-RANGE` | — |
| `#if`, `#unless`, `#each`, `#with`, `lookup` | 13 · verified | `B-TPL-IF-UNLESS-EACH-WITH-LOOKUP` | standard Handlebars block constructs |

## Deliberate deviations

[SPEC §5.5](../SPEC.md#55-deviations-from-wiremock-complete-list-v1) · 54 deviations

The complete list — every place a request that WireMock would accept is answered
differently here, or refused. Each is deliberate and, where it makes sense, has a
configuration knob that restores WireMock's behavior. Read this section before
pointing an existing suite at mockulus: it is where an afternoon goes.

**1.** Journal **disabled by default** (WM: enabled) — verification/journal endpoints return the journal-disabled error until `journal_enabled: true`; deployments serving functional tests that call `verify()` must flip it.

> `B-DEV-DEVIATION-1` · 2 cases · wm: n/a · journal endpoints report the disabled error until journal_enabled is set

**2.** Unmatched-request near-miss diagnostics off by default (WM: on). Knob above.

> `B-DEV-DEVIATION-2` · 4 cases · wm: n/a · the default 404 body carries no near-miss detail; the diagnostics variant adds it

**3.** Non-persistent stubs get a TTL (default 24 h) instead of living until process restart.

> `B-DEV-DEVIATION-3` · 1 case · wm: n/a · a non-persistent stub disappears once its TTL passes

**4.** `mappings/save` = mark-all-persistent (no filesystem write in couchbase mode).

> `B-DEV-DEVIATION-4` · 2 cases · wm: n/a · mappings/save marks every stub persistent instead of writing files

**5.** An **unrecognised** `${json-unit.*}` placeholder inside `equalToJson` is rejected at registration. WM compares it as literal text, which means the stub silently never matches — the failure mode P3 exists to prevent. The documented placeholders are interpreted exactly as WM does (§5.2).

> `B-DEV-DEVIATION-5` · 1 case · wm: n/a · an unrecognised ${json-unit.*} placeholder is rejected at registration rather than compared as text

**6.** Max request body default 10 MiB → 413 beyond (WM: unbounded). Knob `max_body_bytes` (0 = unbounded).

> `B-DEV-DEVIATION-6` · 1 case · wm: n/a · a body beyond the cap is 413 code 1030

**7.** A response carrying `statusMessage` **closes the connection** (WM keeps it alive), and over HTTP/2 the phrase is not conveyed at all (protocol — HTTP/2 has no reason phrase). Both follow from the same cause: `net/http` offers no way to set a reason phrase, so the response is written over a hijacked connection, which HTTP/2 does not permit and which nothing else keeps serving afterwards. Scoped to stubs that set the field; every other response is unaffected.

> `B-DEV-DEVIATION-7` · 1 case · wm: n/a · the reason phrase is present on HTTP/1.1 and the connection closes after it

**8.** `POST /__admin/shutdown` disabled by default.

> `B-DEV-DEVIATION-8` · 2 cases · wm: n/a · the shutdown endpoint is absent by default

**9.** Admin API additionally available on a dedicated ops port; can be removed from the mock port.

> `B-DEV-DEVIATION-9` · 1 case · wm: verified · the admin API answers on the dedicated ops port as well as the mock port

**10.** Journal is eventually consistent: entries visible to verification within ≤ `journal_flush_interval` (default 200 ms) + CB indexing latency; `verify()` should use the polling/timeout forms WM clients already provide. (WM: immediate.)

> `B-DEV-DEVIATION-10` · 1 case · wm: n/a · a verification call sees an entry within the documented window

**11.** Stub propagation across replicas within ≤ `sync_interval` (default 1 s); the replica that handled the admin write reflects it immediately.

> `B-DEV-DEVIATION-11` · 1 case · wm: n/a · the writing pod reflects a write immediately; other pods within sync_interval

**12.** Java-regex constructs unsupported by RE2 run on a fallback engine with a match timeout (§6.6) — pathological backtracking patterns fail closed (non-match + metric) instead of hanging.

> `B-DEV-DEVIATION-12` · 1 case · wm: n/a · a pathological pattern fails closed and is counted

**13.** Rendering errors in templates produce a 500-style body containing the error message, matching WM's render-error-in-body behavior **[DH]** — but parse-time errors are rejected at registration (WM defers all errors to serve time).

> `B-DEV-DEVIATION-13` · 2 cases · wm: n/a · a parse error is rejected at registration while a render error lands in the body

**14.** Unsupported features → 422/404 with error catalog codes (WM would accept some and behave differently — this is the fail-loud contract, D2).

> `B-DEV-DEVIATION-14` · 7 cases · wm: n/a · an unsupported feature is rejected rather than silently ignored

**15.** Fault injection is byte-faithful on HTTP/1.1 only; over HTTP/2 faults degrade to a stream reset. h2c is therefore **off by default** (§12.1).

> `B-DEV-DEVIATION-15` · 1 case (1 Go-native) · wm: n/a · faults are byte-faithful on HTTP/1.1 and h2c is off by default

**16.** Journal-backed verification has bounds: criteria queries scan the newest `journal_query_scan_limit` (10k) entries, and stored bodies are capped at `journal_max_body` (64 KiB) — counts beyond the scan window and body-criteria matches past the cap under-report. Functional suites stay well inside both; load tests keep the journal off.

> `B-DEV-DEVIATION-16` · 1 case · wm: n/a · criteria queries scan a bounded window and bodies are capped

**17.** A `persistent:false` stub whose TTL expires naturally may keep matching on pods that already hold it for up to `resync_interval` (5 m) — expiry doesn't bump the epoch. Explicit deletes/resets propagate within `sync_interval` (§7.4).

> `B-DEV-DEVIATION-17` · 1 case · wm: n/a · an expired ephemeral stub may keep matching until the next resync

**18.** Unmatched-request diagnostic text names mockulus where WM names itself ("…no stub mappings in this **mockulus** instance"). Shape, status and `Content-Type` are identical. Diagnostic text is outside the strict-compat surface (§6.8).

> `B-DEV-DEVIATION-18` · 1 case · wm: n/a · the unmatched body names mockulus, with WireMock's status and Content-Type

**19.** A `response` setting more than one body form is rejected 422 naming both fields. WM accepts it and silently discards all but `body` — accept-and-ignore, which P3 rules out.

> `B-DEV-DEVIATION-19` · 1 case · wm: n/a · a response setting two body forms is 422 naming both fields

**20.** `find-by-metadata` and `remove-by-metadata` consider only stubs that **have** metadata; an explicit `"metadata": null` counts as having none, since WM drops a null on deserialization and the two documents describe the same untagged stub. WM serializes absent metadata to the literal `null` and matches *against* that, so a broad matcher there can remove every untagged stub in the deployment — unacceptable for the shared-deployment cleanup path §1 recommends.

> `B-DEV-DEVIATION-20` · 1 case · wm: n/a · a metadata matcher that would match the literal null leaves untagged stubs alone

**21.** Malformed admin payloads are answered 422/400 with the error envelope where WM raises an unhandled 500 (a `null` `mappings` array on import, a missing `deleteAllNotInImport`). Ours also applies the import atomically — the batch is validated in full before anything is written, where WM's partially applies.

> `B-DEV-DEVIATION-21` · 1 case · wm: n/a · a malformed import is 422 with the error envelope and nothing is written

**22.** An `id` and a `uuid` that **disagree** are rejected 422 naming `/uuid`. WM treats them as one field and lets whichever spelling comes last in the document win, so the stub registers under an identity the client did not necessarily choose and a later `PUT`/`DELETE` on the other id silently hits nothing — or another suite's stub. Resolving a conflict in an identity field by picking one quietly is the failure mode P3 exists to prevent; sending a single spelling reproduces WM exactly.

> `B-DEV-DEVIATION-22` · 1 case · wm: n/a · id and uuid disagreeing is 422 at /uuid, and neither id names a stub afterwards

**23.** `{"absent": false}` is rejected 422 rather than coerced. WM deserializes the field as a presence flag and stores `absent: true` whatever value it was given, so a criterion written to mean "this header must be present" silently becomes its exact opposite and the stub never matches. `{"not": {"absent": true}}` is the supported spelling for "must be present" (§5.2).

> `B-DEV-DEVIATION-23` · 1 case · wm: n/a · absent: false is 422 naming the criterion rather than being coerced to absent: true

**24.** An `id`/`uuid` given as a **24-character base64** encoding of the raw 16 bytes is rejected 422; only the canonical 36-character spelling is accepted. WM's JSON layer takes both and canonicalises base64 to the dashed form, so the id it echoes is not the string the client sent — the same silent rewrite deviation 22 refuses, reached by a different spelling. Every other form WM rejects (dashless, `urn:uuid:`, brace-wrapped) is rejected here too, so nothing registers under an id WM would have refused; sending the canonical spelling reproduces WM exactly.

> `B-DEV-DEVIATION-24` · 1 case · wm: n/a · a base64 id is 422 at /id, and the canonical spelling of the same bytes still registers

**25.** With `ignoreArrayOrder` **and** `ignoreExtraElements` together, an expected array is a subset test we resolve by maximum matching, so we match some arrays WM reports as a mismatch. WM's json-unit search accepts a candidate pairing only when it leaves no actual element unclaimed — a condition the extra elements it was told to ignore make unsatisfiable — so it stops backtracking exactly where those elements appear, and the result depends on the order of an array the stub declared order-irrelevant: expected `["${json-unit.any-number}", 2]` matches `[5,2,9]` and not `[2,5,9]`. Reproducing that would mean a stub matching or not on the order its client happened to serialize. The difference is one-directional — every array WM accepts here we accept identically — and confined to arrays holding an expected element strictly more permissive than another (a placeholder, or an object that `ignoreExtraElements` lets another expected object subsume); with either flag alone, and with literal elements, the two agree exactly (§5.2).

> `B-DEV-DEVIATION-25` · 1 case · wm: n/a · under ignoreArrayOrder + ignoreExtraElements an ambiguous pairing is resolved rather than walked, and the array WireMock refuses matches

**26.** Several matcher keys on **one** matcher document are a conjunction: `{"contains": "a", "doesNotContain": "b"}` requires both. WM honours only the first key its binding visits and discards the rest, so the same document means less there — the stub matches requests its author wrote a criterion to exclude, with nothing said. Conjunction is what a person writing two criteria intends, and it is the direction that refuses more rather than less; a document carrying one key reproduces WM exactly.

> `B-DEV-DEVIATION-26` · 1 case · wm: n/a · two matcher keys on one document both have to hold: satisfying one and failing the other does not match

**27.** `and` and `or` need at least two operands, as in WM — a one-operand form is 422 there and here. (Recorded because the arity is part of the accepted surface, not because the two differ.)

> `B-DEV-DEVIATION-27` · 1 case · wm: n/a · a one-operand and/or is 422 at registration; two operands register

**28.** Near-miss diagnostics list the stubs closest to an unmatched request, ordered by a distance mockulus defines (§6.8). WM's ordering is its own and is not reproduced: the ranking is a debugging aid outside the strict-compat surface, and no matching decision depends on it.

> `B-DEV-DEVIATION-28` · 1 case · wm: n/a · an unmatched request's near misses name the closest stub; no ordering is claimed against WireMock

**29.** Selection among the values of a repeated header or query parameter is plain any-of, including under `caseInsensitive`: the criterion holds when *any* value satisfies it. WM instead picks the value at minimum edit distance and matches that one, so a key carrying a near miss alongside an exact match can answer differently there. Reproducing it would put a distance computation on the matching hot path against §16.3 rule 1, for a corner needing all three of a multi-valued key, `caseInsensitive`, and a sibling at non-zero distance. The date-time matchers make the same difference easier to reach. WireMock picks one value and matches it, and where it cannot rank them it takes the first that parses: a `before` or `after` criterion over `?when=13:00Z&when=11:00Z` answers on the 13:00 alone and refuses, while the two values in the opposite order match. Unparseable values are skipped rather than deciding. `equalToDateTime` is unaffected, because an equality can report a distance and so participates in the ranking — which is what makes the corner an ordering question for two of the three matchers and not for the third. Ours is any-of throughout, so mockulus matches strictly more here: no suite that passes on WireMock can fail on this.

> `B-DEV-DEVIATION-29` · 1 case · wm: n/a · selection among a repeated key's values is plain any-of, including under caseInsensitive

**30.** A response that does not set `statusMessage` gets Go's canonical reason phrase (`500` → `Internal Server Error`, `222` → `status code 222`); WM sends Jetty's (`500` → `Server Error`, `222` → `222`, `420` → `Enhance your Calm`). Matching the table would mean writing every status line by hand, which is what deviation #7's connection-close follows from — so the phrase is only chosen when a stub asks for one. No client is known to read it, and HTTP/2 does not carry it at all.

> `B-DEV-DEVIATION-30` · 1 case · wm: n/a · a response that sets no statusMessage carries Go's canonical reason phrase

**31.** `transformerParameters.disableBodyTemplating` is a mockulus extension, not parity: WM has no such parameter and templates an inline body either way. Its own `disableBodyFileTemplating` guards the `bodyFileName` path only. The extension earns its place — a payload that is itself a Handlebars template is exactly the body a stub wants to exempt — but a stub carrying it renders differently on the two servers. A value that is not a boolean is refused 422 rather than ignored (§10.1).

> `B-DEV-DEVIATION-31` · 1 case · wm: n/a · disableBodyTemplating exempts the body and is refused 422 when it is not a boolean

**32.** `GET /__admin/scenarios` returns `id`, `name`, `state` and `possibleStates`; WM additionally embeds every member stub under `mappings`. A scenario holding a hundred stubs would repeat all hundred inside a listing whose caller wants a state name, and the same documents are one `GET /__admin/mappings` away.

> `B-DEV-DEVIATION-32` · 1 case · wm: n/a · the scenario listing carries possibleStates and no member mappings

**33.** `PUT /__admin/scenarios/{name}/state` naming an unsupported state answers 400 with error code 1031; WM answers 422 with its code 11. Both refuse the write and both name the scenario and the state, so the failure is loud either way; 400 is the reading §5.1 and Appendix B commit to for a path parameter naming a state that does not exist.

> `B-DEV-DEVIATION-33` · 1 case · wm: n/a · an unsupported target state is refused 400 with code 1031

**34.** `Started` is a possible state of every scenario, so `PUT {"state": "Started"}` is always accepted. WM derives `possibleStates` from the stubs alone and refuses to set `Started` when no stub names it — even though it is the state the scenario is in until something moves it, and the state `POST /__admin/scenarios/reset` returns it to. Ours keeps the listing, the request path and the two ways of going back to the beginning agreeing with each other.

> `B-DEV-DEVIATION-34` · 1 case · wm: n/a · Started is always among a scenario's possible states and can always be set

**35.** WireMock's JSON parser is more permissive than `encoding/json`, and mockulus does not follow it. A request body carrying trailing content after a complete document (`{"a":1} tail`), single-quoted member names or values, or `/* */` and `//` comments is parsed there and refused here — so `equalToJson` and `matchesJsonPath` match there and do not here. The same leniency applies to the `equalToJson` *operand*, which registers there and is 422 here. Strictness is the deliberate half: a body that is not JSON is a fact about the request worth reporting, not a shape to guess at.

> `B-DEV-DEVIATION-35` · 2 cases · wm: n/a · a body WireMock's lenient parser accepts and encoding/json rejects does not match here

**36.** A `fixedDelayMilliseconds`, `status`, `body` or `base64Body` that WireMock silently normalises is refused 422 instead — a negative or fractional delay, a `status` given as a string, a non-string `body`. WireMock coerces each and serves something; the value it serves is not the one the author wrote, which is what P3 refuses to do. The cost is real and is stated here rather than hidden: a mappings file using those spellings registers there and not here.

> `B-DEV-DEVIATION-36` · 1 case · wm: n/a · a delay, status or body WireMock coerces is refused 422 here

**37.** A header value outside US-ASCII is compared and emitted as UTF-8, where Jetty renders header bytes as ISO-8859-1 — so a criterion whose operand carries such a character matches the encoding WireMock refuses and refuses the one it matches, and a response header goes out under a different encoding. Nothing on the wire declares an encoding for a header value, so neither is wrong; there is no parameter to read the way there is for a body (§5.2, `Content-Type` charset).

> `B-DEV-DEVIATION-37` · 1 case · wm: n/a · a non-ASCII header value is compared and emitted as UTF-8

**38.** A `jsonBody` is served as the tokens it was registered with — insignificant whitespace between them is dropped, and nothing else is rewritten. WireMock re-serializes the document, so exponent notation becomes plain decimal (`1e2` → `100`), a `\u` escape is emitted as the character it names, and the hex digits of an escape it does keep are normalised to upper case. mockulus preserves the spelling it was given in each of those. The documents are structurally equal either way, which is what §5.6 compares; only the bytes differ.

> `B-DEV-DEVIATION-38` · 1 case · wm: n/a · a jsonBody is served as the bytes it was registered with

**39.** A helper that finds nothing renders nothing: `lookup` with a key absent from its subject, and `substring` over an empty body, both produce the empty string. WireMock renders the whole subject's `toString` for the first (`{tier=gold}`) and `0` for the second. Rendering an internal representation into a response body is not an outcome worth reproducing.

> `B-DEV-DEVIATION-39` · 2 cases · wm: n/a · lookup and substring render nothing when they find nothing

**40.** `importOptions` present without `duplicatePolicy` is treated as the documented default OVERWRITE; WireMock treats the absent policy as IGNORE and keeps the existing stub. When the whole `importOptions` object is omitted the two agree on OVERWRITE — the divergence is only the partially-filled object (§5.1).

> `B-DEV-DEVIATION-40` · 2 cases · wm: n/a · importOptions without duplicatePolicy takes the documented OVERWRITE default

**41.** The stored and echoed mapping is the document that was registered. WireMock fills in defaults on the way out: an absent `response` becomes `{"status": 200}`, a `response` without `status` gains one, and an absent `request` becomes `{"method": "ANY"}`. Serving agrees in all three cases; only the document differs, and under §5.6's subset rule WireMock's added members would fail any diff that touched such a stub. It also normalises values inside the document it echoes: a bare `now` operand on a date-time matcher reads back as `now +0 seconds`, and a truncation value written `FIRST_DAY_OF_MONTH` reads back as `first day of month`. Ours returns the spelling the author wrote, for the reason deviations #22 and #24 refuse to canonicalise an id — rewriting the document a client sent and handing it back as though it were theirs is the quiet substitution P3 exists to prevent, and here it is not even resolving an ambiguity. Neither spelling changes a matching decision on either server.

> `B-DEV-DEVIATION-41` · 1 case · wm: n/a · the echoed mapping is the document registered, with no defaults filled in

**42.** A `matchesJsonPath` whose path selects an array node evaluates the inner matcher against the node, not against its elements: `{"expression": "$.tags", "equalTo": "red"}` does not match `{"tags": ["red"]}`. WireMock cannot distinguish a definite path selecting an array from a list of hits and applies the matcher element-wise. The bare form agrees; this is the nested form only (§6.7).

> `B-DEV-DEVIATION-42` · 1 case · wm: n/a · a definite path selecting an array is matched as a node, not element-wise

**43.** `caseInsensitive` folds by Unicode simple case folding, where Java folds per UTF-16 code unit. The two disagree in both directions: Java folds the Turkish dotted and dotless I to ASCII `i` and `I` and mockulus does not, and mockulus folds supplementary-plane case pairs that Java never folds. Neither is more correct; they are two case-folding definitions.

> `B-DEV-DEVIATION-43` · 2 cases · wm: n/a · caseInsensitive folds by Unicode simple case folding

**44.** A response header registered as a one-element array is stored and echoed as an array. WireMock collapses it to a bare string. Serving agrees — one header line either way — so this is the document only.

> `B-DEV-DEVIATION-44` · 1 case · wm: n/a · a one-element response header array stays an array in the document

**45.** `math` with `/` returns the quotient, including its fraction: `{{math 10 '/' 4}}` renders `2.5`. WireMock rounds half-up to an integer when both operands are integral and renders `3`. Discarding the fraction of a division a template asked for is a surprising default, and the rounded value is one `{{math}}` away for anyone who wants it.

> `B-DEV-DEVIATION-45` · 1 case · wm: n/a · math division keeps the fraction

**46.** `base64Body` is templated when a stub asks for response templating, after the base64 is decoded (§10.1). WireMock templates an inline `body` and a `jsonBody` but never a `base64Body`, so a payload encoded to keep it out of a template renders here and stays literal there.

> `B-DEV-DEVIATION-46` · 1 case · wm: n/a · base64Body is templated after decoding

**47.** Two URL criteria on one stub (`url` with `urlPath`, and so on) are refused 422. WireMock resolves them by a fixed field precedence — `url`, `urlPattern`, `urlPath`, `urlPathPattern`, `urlPathTemplate` — independent of document order, and its echo silently omits the criteria it discarded, so a stub matching on a field its author did not intend reads back as though the others were never written.

> `B-DEV-DEVIATION-47` · 1 case · wm: n/a · two URL criteria on one stub are refused 422

**48.** A response declaring `Content-Type` more than once — as an array of media types, or under two spellings of the name — is refused 422. WireMock accepts it, takes the last value and appends a charset the stub never named, so neither of the declared values reaches the wire. A response carries exactly one Content-Type and its value is one media type: two of them describes a message that cannot be sent, there is no reading of the pair more likely to be what was meant than any other, and refusing is the only answer that does not hand back a header the author did not write. One value, in either the bare or the one-element-array spelling, is unaffected, and every other header may repeat freely.

> `B-DEV-DEVIATION-48` · 1 case · wm: n/a · a response declaring Content-Type more than once is refused 422

**49.** A date-time **operand or modifier spelling that can never match** is refused 422. WireMock accepts thirteen operand spellings that register and then fail every request with no diagnostic — a colon-less `+0300`, a time-only value, a bare epoch, an empty string, `now+2days`, `now + 2 days`, a doubled space, trailing text, a whitespace-padded keyword — and it silently drops a modifier key it does not recognise, so a misspelled `truncateExpectedTo` reads as though it had been applied. Both are the accept-and-ignore failure P3 exists to prevent, and both are one claim about one feature: a criterion the author wrote is either honoured or refused, never quietly discarded. Sending a spelling WireMock can actually act on reproduces it exactly.

> `B-DEV-DEVIATION-49` · 2 cases · wm: n/a · a date-time operand or modifier spelling that can never match is refused 422 rather than registered

**50.** A truncation parameter that **could not take effect** is refused 422 rather than accepted. `truncateExpected` does nothing on a literal expected value — WireMock applies it only to a now-relative one — and `truncateActual` does nothing when `actualFormat` reads the value with a pattern, because WireMock truncates only a value that parsed to a zoned instant. Both are detectable when the stub registers, so both are refused there. The case that is *not* detectable then — a zoneless or date-only ISO actual, which WireMock also skips — is mirrored rather than refused: it depends on the request, and truncating anyway would match where WireMock does not.

> `B-DEV-DEVIATION-50` · 1 case · wm: n/a · a truncation parameter that could not take effect is refused 422

**51.** `equalToDateTime` against a **bare date matches that whole day**. WireMock reads a date-only expected as midnight, so `equalToDateTime: "2021-06-14"` matches only `00:00:00` and excludes almost every moment of the day it names — an answer nobody writing that criterion means. The widening is deliberately confined to equality: `before` and `after` keep midnight, because widening those would refuse requests WireMock accepts. So the difference is one-directional — every request WireMock matches, mockulus matches identically — and no suite that passes there can fail here.

> `B-DEV-DEVIATION-51` · 1 case · wm: n/a · equalToDateTime against a bare date matches every moment of that day, while before and after keep midnight

**52.** A **non-numeric actual under `actualFormat: unix` or `epoch` is a non-match**, where WireMock answers 500. Its parse is an unguarded `Long.parseLong`, so an empty value, a decimal or any other non-integer escapes as a `NumberFormatException` and reaches the client as a Jetty error page — from a header, a query parameter or a cookie alike. A request that is not what a criterion asked for is a fact about the request, which is how every other matcher treats input it cannot read (§6.7), and mock traffic is untrusted input that must not be able to produce a server error (§17).

> `B-DEV-DEVIATION-52` · 1 case · wm: n/a · a non-numeric actual under actualFormat unix or epoch is a non-match, not a server error

**53.** `actualFormat`, `truncateExpected`, `truncateActual` and `applyTruncationLast` are refused 422 **anywhere they have no date-time matcher to modify**. WireMock accepts all four beside `equalTo` or `contains`, where a date pattern means nothing, and as a sibling of `and`/`or`/`not`, where the format never reaches the leaves that would use it — silently, so a stub author who writes it once and expects it to apply gets neither the behaviour nor a diagnostic. Repeating the modifier inside each leaf works on both servers.

> `B-DEV-DEVIATION-53` · 2 cases · wm: n/a · actualFormat and the truncation parameters are refused where there is no date-time matcher to modify

**54.** `pathParameters` **without a `urlPathTemplate`** is refused 422, as is a parameter naming a variable the template does not bind. WireMock accepts the first and drops the whole block, so an unsatisfiable criterion registers and the stub matches *every* request — the widest possible failure, arrived at silently. It refuses nothing in the second case either and simply never matches. Neither is a criterion the author could have meant, and a stub whose path criteria are ignored is one that intercepts traffic belonging to somebody else.

> `B-DEV-DEVIATION-54` · 1 case · wm: n/a · pathParameters without a urlPathTemplate is refused 422 rather than dropped

## Beyond the WireMock surface

A single-node oracle has nothing to diff these against: an operational contract
under a store outage, an error code, a configuration key, a Prometheus collector.
Their expectations come from the spec, and the case pins the spec — which is what
`wm: n/a` records.

### Degraded modes

[SPEC §4.6](../SPEC.md#46-degraded-modes-explicit-contract) · 4 behaviors

What the server does when the store is not there. The half worth reading is what keeps working.

| Condition | Evidence | Behavior | Notes |
|---|---|---|---|
| CB down, snapshot loaded | 7 · n/a | `B-DEGRADED-CB-DOWN-SNAPSHOT-LOADED` | Keep serving mock traffic from the last snapshot. Admin **writes** → 503 `storeUnavailable`. Scenario-stub requests → 500 `scenarioUnavailable` (correctness over availability; plain stubs unaffected). Journal entries dropped + counted. `/readyz` stays 200 (we can serve), `/healthz` stays 200; store health exposed via metric + `/__admin/health` detail. |
| CB down at boot | 1 · n/a (1 Go-native) | `B-DEGRADED-CB-DOWN-AT-BOOT` | Not ready; retry forever (§4.4). |
| Store read fails during rebuild (`LoadAll` error) | 1 · n/a | `B-DEGRADED-STORE-READ-FAILS-DURING-REBUILD-LOADALL-ERROR` | Keep previous snapshot; log error + `mockulus_snapshot_reload_failures_total`; retry next poll tick. Individual bad/undecodable/uncompilable docs never abort a build — quarantined per §6.9. |
| Journal queue full | 1 · n/a | `B-DEGRADED-JOURNAL-QUEUE-FULL` | Drop entry, increment `mockulus_journal_dropped_total`. Never block or slow the hot path. |

### Error catalog

[SPEC Appendix B](../SPEC.md#appendix-b--error-catalog) · 14 behaviors

Every rejection carries one of these in a WireMock-shaped error envelope, with a JSON pointer at the offending field. A 422 lists **all** problems in one response.

| Code | HTTP | Evidence | Behavior | Notes |
|---|---|---|---|---|
| 10 | 422 | 29 · verified | `B-ERR-10` | Malformed JSON / schema violation (WM parity code, verified) |
| 109 | 422 | 6 · verified | `B-ERR-109` | Stub id already exists on create (WM parity code) |
| 1000 | 422 | 8 · n/a | `B-ERR-1000` | Unsupported stub feature (pointer names the field) |
| 1001 | 404 | 1 · n/a | `B-ERR-1001` | Unsupported admin endpoint (body links ROADMAP) |
| 1002 | 422 | 4 · n/a | `B-ERR-1002` | Unknown template helper / template parse error |
| 1003 | 422 | 5 · n/a | `B-ERR-1003` | Regex does not compile (both engines) |
| 1004 | 422 | 2 · n/a | `B-ERR-1004` | Unknown transformer name |
| 1005 | 422 | 1 · n/a | `B-ERR-1005` | Unknown settings key |
| 1010 | 500 | 2 · n/a | `B-ERR-1010` | Journal disabled (WM parity shape **[DH]**) |
| 1020 | 503 | 9 · n/a | `B-ERR-1020` | Store unavailable (admin writes during CB outage) |
| 1021 | 500 | 1 · n/a | `B-ERR-1021` | Scenario state unavailable (CB outage, scenario stub) |
| 1022 | 500 | 3 · n/a | `B-ERR-1022` | Stub's `bodyFileName` has no corresponding file (serve time; §6.9) |
| 1030 | 413 | 1 · n/a | `B-ERR-1030` | Request body exceeds `max_body_bytes` |
| 1031 | 400 | 1 · n/a | `B-ERR-1031` | Unknown scenario / invalid scenario state |

### Configuration keys

[SPEC §13](../SPEC.md#13-configuration-reference) · 43 behaviors

Precedence is env var > YAML file > default; the env spelling is `MOCKULUS_` plus the key in upper snake case.

| Key | Default | Evidence | Behavior | Notes |
|---|---|---|---|---|
| `port` | `8080` | 1 · verified | `B-CFG-PORT` | Mock listener (`0` binds an ephemeral port) |
| `admin_port` | `9090` | 1 · n/a | `B-CFG-ADMIN-PORT` | Admin/ops listener (`0` binds an ephemeral port) |
| `admin_on_mock_port` | `true` | 2 · verified | `B-CFG-ADMIN-ON-MOCK-PORT` | Serve `/__admin` on the mock port (compat) |
| `store` | `auto` | 4 · n/a | `B-CFG-STORE` | `auto` (couchbase if connstr set, else memory) \| `couchbase` \| `memory` \| `file` |
| `couchbase.connstr` | — | 1 · n/a | `B-CFG-COUCHBASE-CONNSTR` | e.g. `couchbase://cb.mockulus.svc` |
| `couchbase.username` / `couchbase.password` | — | 2 · n/a | `B-CFG-COUCHBASE-USERNAME-COUCHBASE-PASSWORD` | Password also via `_FILE` variant for mounted secrets |
| `couchbase.bucket` / `couchbase.scope` | `mockulus` / `_default` | 1 · n/a | `B-CFG-COUCHBASE-BUCKET-COUCHBASE-SCOPE` | — |
| `couchbase.durability` | `none` | 1 · n/a | `B-CFG-COUCHBASE-DURABILITY` | `none` \| `majority` |
| `couchbase.manage_bucket` | `true` | 1 · n/a | `B-CFG-COUCHBASE-MANAGE-BUCKET` | Auto-create collections/indexes at boot |
| `couchbase.kv_timeout` / `couchbase.query_timeout` | `2500ms` / `10s` | ○ | `B-CFG-COUCHBASE-KV-TIMEOUT-COUCHBASE-QUERY-TIMEOUT` | — |
| `scenario_kv_timeout` | `250ms` | 1 · n/a | `B-CFG-SCENARIO-KV-TIMEOUT` | Budget for scenario reads/CAS on the request path |
| `file.root` | — | 6 · n/a | `B-CFG-FILE-ROOT` | `file` store: dir containing `mappings/` and `__files/` |
| `sync_interval` | `1s` | 1 · n/a | `B-CFG-SYNC-INTERVAL` | Epoch poll interval (min `100ms`) |
| `resync_interval` | `5m` | 1 · n/a | `B-CFG-RESYNC-INTERVAL` | Unconditional full reload (expiry sweep, self-heal) |
| `ephemeral_stub_ttl` | `24h` | 1 · n/a | `B-CFG-EPHEMERAL-STUB-TTL` | TTL for `persistent:false` stubs (`0` = none) |
| `start_without_store` | `false` | 1 · n/a | `B-CFG-START-WITHOUT-STORE` | Become ready with empty snapshot if store is down at boot |
| `journal_enabled` | `false` | 1 · n/a | `B-CFG-JOURNAL-ENABLED` | Master switch |
| `journal_ttl` | `30m` | 1 · n/a | `B-CFG-JOURNAL-TTL` | Entry TTL |
| `journal_max_body` | `64KiB` | 1 · n/a | `B-CFG-JOURNAL-MAX-BODY` | Per-entry stored body cap |
| `journal_buffer` / `journal_buffer_bytes` | `8192` / `64MiB` | 1 · n/a | `B-CFG-JOURNAL-BUFFER-JOURNAL-BUFFER-BYTES` | Queue caps — entry count and total bytes, whichever first |
| `journal_flush_workers` / `journal_batch_size` / `journal_flush_interval` | `4` / `500` / `200ms` | ○ | `B-CFG-JOURNAL-FLUSH-WORKERS-JOURNAL-BATCH-SIZE-JOURNAL-FLUSH-INTER` | Writer tuning (bulk KV) |
| `journal_query_scan_limit` | `10000` | 1 · n/a | `B-CFG-JOURNAL-QUERY-SCAN-LIMIT` | Criteria-query scan guard |
| `templating_enabled` | `wm-compat` | 3 · n/a | `B-CFG-TEMPLATING-ENABLED` | `wm-compat` (mirror pinned WM activation, §10.1) \| `on` (force global) \| `off` |
| `template_max_output_bytes` | `10MiB` | 1 · n/a | `B-CFG-TEMPLATE-MAX-OUTPUT-BYTES` | — |
| `max_body_bytes` | `10MiB` | 2 · n/a | `B-CFG-MAX-BODY-BYTES` | Request body cap (`0` = unbounded) |
| `regex_timeout` | `100ms` | 1 · n/a | `B-CFG-REGEX-TIMEOUT` | regexp2 fallback match timeout |
| `diagnostics_on_unmatched` | `false` | 6 · n/a | `B-CFG-DIAGNOSTICS-ON-UNMATCHED` | Near-miss detail in 404s |
| `admin_auth_token` | — | 2 · n/a | `B-CFG-ADMIN-AUTH-TOKEN` | If set, admin API requires `Authorization: Token <t>` (§17) |
| `admin_shutdown_enabled` | `false` | 2 · n/a | `B-CFG-ADMIN-SHUTDOWN-ENABLED` | Enable `POST /__admin/shutdown` |
| `tls_cert_file` / `tls_key_file` | — | 4 · n/a (3 Go-native) | `B-CFG-TLS-CERT-FILE-TLS-KEY-FILE` | Enable TLS on mock port |
| `h2c_enabled` | `false` | 1 · n/a (1 Go-native) | `B-CFG-H2C-ENABLED` | Cleartext HTTP/2 on mock port (off by default — fault fidelity, §12.5) |
| `write_slack` | `10s` | ○ | `B-CFG-WRITE-SLACK` | Mock-port per-response write deadline = configured delay + this slack |
| `shutdown_drain` / `shutdown_timeout` | `5s` / `15s` | 3 · n/a (2 Go-native) | `B-CFG-SHUTDOWN-DRAIN-SHUTDOWN-TIMEOUT` | §4.5 |
| `log.level` / `log.format` | `info` / `json` | 1 · n/a | `B-CFG-LOG-LEVEL-LOG-FORMAT` | `text` for local dev |
| `log.requests` | `false` | 1 · n/a | `B-CFG-LOG-REQUESTS` | Per-request access logs (hot path — keep off under load) |
| `log.request_sample_n` | `100` | 1 · n/a | `B-CFG-LOG-REQUEST-SAMPLE-N` | With `log.requests`, log every Nth request |
| `metrics_enabled` | `true` | 2 · n/a | `B-CFG-METRICS-ENABLED` | — |
| `tracing.enabled` | `false` | 2 · n/a (2 Go-native) | `B-CFG-TRACING-ENABLED` | Export OpenTelemetry traces (off by default; §14.3) |
| `tracing.endpoint` | — | 3 · n/a (3 Go-native) | `B-CFG-TRACING-ENDPOINT` | OTLP/HTTP collector as `host:port` (e.g. `otel-collector:4318`); required when enabled |
| `tracing.insecure` | `false` | 2 · n/a (2 Go-native) | `B-CFG-TRACING-INSECURE` | Send over plain HTTP rather than HTTPS |
| `tracing.headers` | — | 1 · n/a (1 Go-native) | `B-CFG-TRACING-HEADERS` | Exporter headers as `k=v,k=v` (e.g. an ingestion token) |
| `tracing.sample_ratio` | `0.1` | 1 · n/a (1 Go-native) | `B-CFG-TRACING-SAMPLE-RATIO` | Sampling ratio for traces this pod starts itself (0–1); a caller's decision always wins |
| `tracing.service_name` | `mockulus` | 1 · n/a (1 Go-native) | `B-CFG-TRACING-SERVICE-NAME` | `service.name` reported on exported spans |

Rows marked ○ have no distinct observable of their own — a tuning knob whose
effect is asserted through the behavior it protects. The exemption is reviewed, not
automatic:

- `couchbase.kv_timeout / couchbase.query_timeout` — SDK timeout tuning with no distinct black-box observable of its own; the behavior it protects is covered by the degraded-mode cases of §4.6
- `journal_flush_workers / journal_batch_size / journal_flush_interval` — writer throughput tuning; the observable contract is journal visibility, covered by the §11.4 consistency prose contract
- `write_slack` — write-deadline headroom above a stub's own delay; the observable is that a delayed response still completes, covered by the fixedDelayMilliseconds case

### Metrics

[SPEC §14.1](../SPEC.md#141-metrics-prometheus-metrics-on-admin-port) · 22 behaviors

Prometheus exposition on the admin port's `/metrics`. Low-cardinality by design: no per-stub labels, so a 10k-stub deployment does not mint 10k series. The Type column reproduces what the spec's collector block states, and that block names a type only where it declares one collector per line — `/metrics` itself carries a `# TYPE` line for every series either way.

| Collector | Labels | Type | Evidence | Behavior |
|---|---|---|---|---|
| `mockulus_build_info` | `{version,go_version}` | `gauge` | 1 · n/a | `B-METRIC-BUILD-INFO` |
| `mockulus_http_requests_total` | `{matched="true\|false",code}` | `counter` | 1 · n/a | `B-METRIC-HTTP-REQUESTS-TOTAL` |
| `mockulus_http_request_duration_seconds` | `{matched}` | `histogram` | 1 · n/a | `B-METRIC-HTTP-REQUEST-DURATION-SECONDS` |
| `mockulus_admin_requests_total` | `{endpoint_group,code}` | `counter` | 1 · n/a | `B-METRIC-ADMIN-REQUESTS-TOTAL` |
| `mockulus_snapshot_stubs` | — | `gauge` | 1 · n/a | `B-METRIC-SNAPSHOT-STUBS` |
| `mockulus_snapshot_epoch` | — | `gauge` | 1 · n/a | `B-METRIC-SNAPSHOT-EPOCH` |
| `mockulus_snapshot_reloads_total` | `{trigger="admin\|epoch\|resync"}` | `counter` | 1 · n/a | `B-METRIC-SNAPSHOT-RELOADS-TOTAL` |
| `mockulus_snapshot_reload_duration_seconds` | — | `histogram` | 1 · n/a | `B-METRIC-SNAPSHOT-RELOAD-DURATION-SECONDS` |
| `mockulus_snapshot_reload_failures_total` | — | `counter` | 1 · n/a | `B-METRIC-SNAPSHOT-RELOAD-FAILURES-TOTAL` |
| `mockulus_snapshot_quarantined_total` | `{reason}` | `counter` | 2 · n/a | `B-METRIC-SNAPSHOT-QUARANTINED-TOTAL` |
| `mockulus_store_operation_duration_seconds` | `{op}` | `histogram` | 1 · n/a | `B-METRIC-STORE-OPERATION-DURATION-SECONDS` |
| `mockulus_store_errors_total` | `{op}` | `counter` | 2 · n/a | `B-METRIC-STORE-ERRORS-TOTAL` |
| `mockulus_scenario_reads_total` | — | — | 1 · verified | `B-METRIC-SCENARIO-READS-TOTAL` |
| `mockulus_scenario_cas_retries_total` | — | — | 1 · n/a (1 Go-native) | `B-METRIC-SCENARIO-CAS-RETRIES-TOTAL` |
| `mockulus_scenario_transition_conflicts_total` | — | — | 1 · n/a (1 Go-native) | `B-METRIC-SCENARIO-TRANSITION-CONFLICTS-TOTAL` |
| `mockulus_journal_enqueued_total` | — | — | 1 · n/a | `B-METRIC-JOURNAL-ENQUEUED-TOTAL` |
| `mockulus_journal_dropped_total` | — | — | 1 · n/a | `B-METRIC-JOURNAL-DROPPED-TOTAL` |
| `mockulus_journal_flush_duration_seconds` | — | — | 1 · n/a | `B-METRIC-JOURNAL-FLUSH-DURATION-SECONDS` |
| `mockulus_template_render_errors_total` | — | — | 9 · n/a (1 Go-native) | `B-METRIC-TEMPLATE-RENDER-ERRORS-TOTAL` |
| `mockulus_regex_timeouts_total` | — | — | 1 · n/a | `B-METRIC-REGEX-TIMEOUTS-TOTAL` |
| `mockulus_match_candidates` | — | `histogram` | 1 · n/a | `B-METRIC-MATCH-CANDIDATES` |
| `mockulus_trace_export_failures_total` | — | `counter` | 1 · n/a (1 Go-native) | `B-METRIC-TRACE-EXPORT-FAILURES-TOTAL` |

## Behaviors stated in prose

7 contracts are stated as prose rather than as a table, so they cannot be derived
mechanically the way every row above was. They are catalogued by hand against a hash
of the section they encode: editing that prose fails the gate until a person re-reads
it and re-syncs the entry. All three are the distributed form of something a
single-process server gets for free, which is why none has an oracle.

| Contract | Section | Evidence | Behavior |
|---|---|---|---|
| Epoch polling propagates stub changes to every replica, level-triggered and coalesced, over a bulk read consistent with every write the epoch accounts for | [§8](../SPEC.md#8-cluster-synchronization) | 4 · n/a | `B-PROSE-SYNC-PROPAGATION` |
| Scenario state is distributed, starts at Started, gates matching and transitions under CAS | [§9](../SPEC.md#9-scenarios-stateful-mocks) | 1 · n/a | `B-PROSE-SCENARIO-SEMANTICS` |
| Journal entries become visible to verification eventually, and never block the request path | [§11](../SPEC.md#11-request-journal--verification) | 1 · n/a | `B-PROSE-JOURNAL-CONSISTENCY` |
| A served request's phases become child spans of its server span, and only when the phase ran | [§14](../SPEC.md#144-span-model--correlation) | 1 · n/a (1 Go-native) | `B-PROSE-TRACING-SPAN-MODEL` |
| Background work roots its own trace rather than joining the request that triggered it | [§14](../SPEC.md#144-span-model--correlation) | 1 · n/a (1 Go-native) | `B-PROSE-TRACING-BACKGROUND-ROOT` |
| A journal entry carries the trace id of the request it records, when that request was sampled | [§14](../SPEC.md#144-span-model--correlation) | 1 · n/a (1 Go-native) | `B-PROSE-TRACING-CORRELATION` |
| Compatibility truth is established differentially: wm:verified cases are replayed against the pinned oracle and diffed with subset semantics, over a fresh connection per case | [§5.6](../SPEC.md#56-differential-compatibility-verification-the-compat-tiebreaker) | 1 · verified | `B-PROSE-DIFFERENTIAL-ORACLE` |

<!-- END GENERATED MATRIX -->
