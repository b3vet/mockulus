# Deviations from WireMock

Mockulus answers differently from WireMock in 57 places. This page is all of
them, grouped by what you are doing when you hit one, with what to expect and
what to do about it.

A deviation is a decision, not a defect. Each one was taken because the
alternative cost something specific, and in almost every case the cost is one of
two things. Either reproducing WireMock exactly would put work on the hot path
that a mock server serving 50k requests per second cannot afford — a distance
computation per unmatched request, a write per served request, a per-value edit
distance on a repeated header. Or WireMock resolves an ambiguous stub by picking
an interpretation quietly, and the stub then means something its author did not
write: an identity field it did not choose, a criterion that became its own
opposite, a delay it did not ask for. Mockulus refuses those at registration
rather than serving them (principle P3 — see [SPEC §2](../SPEC.md#2-guiding-principles)).
Where restoring WireMock's behavior is possible, there is a configuration key,
and it is named in the entry.

The numbered source is [SPEC §5.5](../SPEC.md#55-deviations-from-wiremock-complete-list-v1);
every number in it appears below, tagged inline as `#n`, so you can read either
document and cross-reference the other.

Two things this page is not. It is not the list of unsupported features — those
are in the [compatibility matrix](compatibility.md), and each one is a `422` at
registration with a pointer at [ROADMAP.md](../ROADMAP.md), never a silent
non-match. And it is not a migration guide; if you are moving a suite across,
start with [migrating from WireMock](migrating-from-wiremock.md) and come back
here for the detail.

Everything stated below about **mockulus** is a transcript from a running
instance. Everything stated about **WireMock** is what the differential harness
records against the pinned image — `wiremock/wiremock:3.13.2`, held in
`test/e2e/WIREMOCK_VERSION` — and diffed on every pull request
([SPEC §5.6](../SPEC.md#56-differential-compatibility-verification-the-compat-tiebreaker)).

## Running the examples

Every command below was run against a locally started instance backed by the
memory store. Where a deviation needs a non-default setting, the command that
starts that instance is shown with it. Start one and point two variables at it:

```console
$ mockulus &
$ export MOCK=http://localhost:8080 ADMIN=http://localhost:9090
```

The variables exist because the mock port and the admin port answer different
things, and several deviations are precisely about which port answers what. Set
them to wherever your instance is; every command below is written against them.
Stub ids in the transcripts are server-generated, so yours will differ.

---

## The three that change day one

These three change how an existing suite behaves before you have looked at a
single mapping. Read them even if you read nothing else.

### #1 — The journal is off by default

WireMock records every request it serves and `verify()` reads that recording.
Mockulus records nothing until you ask it to. Until then every journal and
verification endpoint answers with the journal-disabled error:

```console
$ curl -s "$ADMIN/__admin/requests"
{"errors":[{"code":1010,"title":"Journal disabled","detail":"the request journal is disabled; set journal_enabled to record and verify requests"}]}

$ curl -s -X POST "$ADMIN/__admin/requests/count" -d '{"method":"GET","urlPath":"/api/users/7"}'
{"errors":[{"code":1010,"title":"Journal disabled","detail":"the request journal is disabled; set journal_enabled to record and verify requests"}]}
```

A suite that calls `verify()` fails on its first assertion, loudly and with the
same message every time — which is the intended shape of the failure. Turn the
journal on:

```console
$ MOCKULUS_JOURNAL_ENABLED=true mockulus &
$ curl -s -X POST "$ADMIN/__admin/requests/count" -d '{"method":"GET","urlPath":"/api/users/7"}'
{"count":1,"requestJournalDisabled":false}
```

**Why.** Always-on journaling at the throughput this server is built for means
one write per request. WireMock's journal is an unbounded in-JVM list and is a
large part of why it collapses under load; a distributed journal that is always
on would move that collapse into Couchbase (decision D3,
[SPEC §3](../SPEC.md#3-architecture-decision-record)). Off by default keeps the
serving path free for the deployments that never verify anything — load rigs and
shared environment mocks — and functional suites pay for it explicitly.

**What to do.** Turn it on for CI and functional deployments. Leave it off for
load testing. Note that it changes the consistency model of your assertions:
see [#10](#10--the-journal-is-eventually-consistent) below.

### #2, #18 — Near-miss diagnostics are off by default, and name mockulus

WireMock computes near misses on every unmatched request and puts them in the
404 body. Mockulus computes nothing:

```console
$ curl -s "$MOCK/api/users/8"
Request was not matched
```

Set `diagnostics_on_unmatched` and the detail comes back:

```console
$ MOCKULUS_DIAGNOSTICS_ON_UNMATCHED=true mockulus &
$ curl -s "$MOCK/api/users/8"
Request was not matched
Closest stubs:

  get user (c33c5b81-16b0-48a3-ae8b-f54cf22a1094)
    url: expected /api/users/7, got /api/users/8
```

The detail is **appended**, never substituted: the status, the `Content-Type`
(`text/plain;charset=UTF-8`, WireMock's exact spelling, no space after the
semicolon) and the first line are identical either way, so only the body
distinguishes a debugging deployment from a production one. An assertion on the
404 body's first line holds in both modes.

There are two first lines, and which one you get depends on the snapshot, not
the request. An empty instance says so:

```console
$ curl -s "$MOCK/api/users/8"
No response could be served as there are no stub mappings in this mockulus instance.
```

That sentence is `#18`: WireMock names itself in the same place, and mockulus
names itself. Shape, status and `Content-Type` are identical; only the product
name differs. Diagnostic text is deliberately outside the strict-compatibility
surface ([SPEC §6.8](../SPEC.md#68-near-miss-scoring)), so a suite asserting on
that exact string needs one edit.

**Why.** Scoring every non-matching stub against every unmatched request is a
Levenshtein distance per string criterion, on the request path, for a result
almost every unmatched request throws away. With the flag off nothing is scored
and nothing is allocated.

**What to do.** Turn it on while you are debugging a suite, off for load. The
near-miss *endpoints* — `/__admin/requests/unmatched/near-misses`,
`/__admin/near-misses/request`, `/__admin/near-misses/request-pattern` — always
work regardless of the flag, because they compute on demand at admin-request
time where the cost is nobody's hot path.

### #3 — Non-persistent stubs expire

In WireMock a stub lives until the process restarts. In mockulus a stub without
`"persistent": true` is stored with a TTL, `ephemeral_stub_ttl`, default 24
hours. Below, the TTL is shortened to three seconds to make it observable:

```console
$ MOCKULUS_EPHEMERAL_STUB_TTL=3s MOCKULUS_RESYNC_INTERVAL=1s mockulus &

$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/eph"},"response":{"status":200}}'
201
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"persistent":true,"request":{"url":"/per"},"response":{"status":200}}'
201

$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/eph"; curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/per"
200
200
$ sleep 5
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/eph"; curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/per"
404
200
```

**Why.** A long-lived cluster is not a JVM you restart between suites. Every CI
run that registers stubs and does not clean up leaves them behind forever, and
"forever" in a shared deployment is a stub set nobody can reason about. The TTL
is the cluster equivalent of WireMock's process lifetime.

**What to do.** Nothing, for a suite that registers stubs per test and finishes
inside a day. For mocks that are meant to outlive a day — a downstream service
mocked in a dev cluster — set `"persistent": true`, which also makes them
survive `POST /__admin/mappings/reset`. Set `ephemeral_stub_ttl: 0` to disable
the TTL entirely. Expiry has a propagation subtlety across replicas: see
[#17](#17--an-expired-stub-can-outlive-its-ttl-on-a-pod).

---

## Registering a mapping

Everything in this section happens at `POST`/`PUT` time, before any traffic
reaches the mock port. Each one is a `422` carrying an error envelope whose
`source.pointer` names the offending field, and one response reports every
problem it found rather than stopping at the first — a mappings file is fixed in
one round rather than one field per attempt.

### #14 — Unsupported features are refused, not ignored

The umbrella rule. A field mockulus does not implement is `422` code `1000`
naming the pointer and linking the roadmap; an admin path it does not implement
is `404` code `1001`.

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/soap","bodyPatterns":[{"matchesXPath":"//order"}]},"response":{"status":200}}'
{"errors":[{"code":1000,"source":{"pointer":"/request/bodyPatterns/0/matchesXPath"},"title":"Unsupported feature","detail":"matchesXPath (XPath matching) is not supported in mockulus v1 — see ROADMAP.md"}]}
```

WireMock accepts some of these and behaves in a way your suite may or may not
have noticed. The list of what is unsupported is the
[compatibility matrix](compatibility.md); the point here is that you find out at
load time, in one command, for the whole file.

### #22 — An `id` and a `uuid` that disagree

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"id":"11111111-1111-1111-1111-111111111111","uuid":"22222222-2222-2222-2222-222222222222","request":{"url":"/c"},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/uuid"},"title":"Malformed request","detail":"id and uuid are aliases and must not disagree"}]}
```

They are aliases for one field. WireMock lets whichever spelling comes last in
the document win, so the stub registers under an identity the client did not
necessarily choose, and a later `PUT` or `DELETE` on the other id silently hits
nothing — or another suite's stub in a shared deployment. **Send one spelling
and mockulus and WireMock agree exactly.**

### #24 — A base64-encoded `id`

Only the canonical 36-character spelling is accepted:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"id":"EREREREREREREREREREREQ==","request":{"url":"/u1"},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/id"},"title":"Malformed request","detail":"id must be a canonical 36-character UUID"}]}
```

WireMock's JSON layer accepts the 24-character base64 encoding of the raw 16
bytes and canonicalises it to the dashed form, so the id it echoes is not the
string the client sent — the same silent rewrite as #22, reached by a different
spelling. Every other form WireMock rejects (dashless, `urn:uuid:`,
brace-wrapped) is rejected here too, so nothing registers under an id WireMock
would have refused. The canonical spelling is parsed case-insensitively and
canonicalised to lower case, exactly as there.

### #23 — `{"absent": false}`

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/d","headers":{"X-Trace-Id":{"absent":false}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-Trace-Id/absent"},"title":"Malformed request","detail":"\"absent\": false is not a matcher; omit the criterion instead"}]}
```

WireMock deserializes `absent` as a presence flag and stores `absent: true`
whatever value it was given. A criterion written to mean "this header must be
present" becomes its exact opposite and the stub never matches — the single most
expensive kind of silence, because the stub looks correct in the file. The
supported spelling for "must be present" is `{"not": {"absent": true}}`, and it
works as you would expect — here against a stub whose `request.headers` is
`{"X-P": {"not": {"absent": true}}}`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-P: anything' "$MOCK/pres"
200
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/pres"
404
```

### #47 — Two URL criteria on one stub

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/a?x=1","urlPath":"/a"},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request"},"title":"Malformed request","detail":"only one URL criterion may be given, found url, urlPath"}]}
```

WireMock resolves the pair by a fixed field precedence — `url`, `urlPattern`,
`urlPath`, `urlPathPattern`, `urlPathTemplate` — independent of document order,
and its echo silently omits the criteria it discarded. A stub matching on a
field its author did not intend then reads back as though the others were never
written, which makes the mistake invisible in exactly the place you would look
for it.

### #54 — `pathParameters` without a `urlPathTemplate`

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"urlPath":"/orders/123","pathParameters":{"id":{"equalTo":"123"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/pathParameters"},"title":"Malformed request","detail":"pathParameters needs a urlPathTemplate to bind against"}]}
```

A parameter naming a variable the template does not bind is refused the same
way, and the message names the template it looked in:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"urlPathTemplate":"/orders/{id}","pathParameters":{"orderId":{"equalTo":"123"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/pathParameters/orderId"},"title":"Malformed request","detail":"the template \"/orders/{id}\" binds no variable named \"orderId\""}]}
```

WireMock accepts the first and drops the whole `pathParameters` block, so the
criterion the author wrote is never evaluated and the stub answers everything its
remaining criteria admit — for a stub whose path parameters *were* its criteria,
that is every request. It is the widest failure on this page and it happens in
silence. The second case fails the other way there: nothing is refused and the
stub never matches at all. Neither is a criterion the author could have meant,
and a stub whose path criteria are ignored is one that intercepts traffic
belonging to somebody else.

Pair the block with a template that binds its names and the two servers agree
exactly:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"urlPathTemplate":"/orders/{id}","pathParameters":{"id":{"equalTo":"123"}}},"response":{"status":200}}'
201
```

There is no configuration key for this one, and none is wanted: the accepting
behavior is the one that matches other people's traffic.

### #19 — More than one body form on a response

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/two"},"response":{"body":"a","jsonBody":{"b":1}}}'
{"errors":[{"code":10,"source":{"pointer":"/response"},"title":"Malformed request","detail":"exactly one body form may be set, found body, jsonBody"}]}
```

`body`, `jsonBody`, `base64Body` and `bodyFileName` are four spellings of one
thing. WireMock accepts several and silently discards all but `body`.

### #36 — Values WireMock silently normalises

A `status` given as a string, a non-string `body`, a negative or fractional
`fixedDelayMilliseconds`, a non-string `base64Body`: WireMock coerces each and
serves something, and the something is not what the author wrote.

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/n1"},"response":{"status":"200","body":42,"fixedDelayMilliseconds":-5}}'
{"errors":[{"code":10,"source":{"pointer":"/response/status"},"title":"Malformed request","detail":"status must be an integer"},{"code":10,"source":{"pointer":"/response/body"},"title":"Malformed request","detail":"body must be a string"},{"code":10,"source":{"pointer":"/response/fixedDelayMilliseconds"},"title":"Malformed request","detail":"fixedDelayMilliseconds must not be negative"}]}
```

The cost of this one is real and worth stating plainly: a mappings file using
those spellings registers on WireMock and does not register here. That is the
sharpest edge on this page for a large existing corpus. The fix is mechanical
and the `422` tells you every location in one pass.

### #35 — Strict JSON parsing

WireMock's JSON parser is more permissive than Go's. Trailing content after a
complete document, single-quoted member names or values, and `/* */` or `//`
comments are all parsed there. Here they are refused — as an `equalToJson`
operand, at registration:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/j3","bodyPatterns":[{"equalToJson":"{a:1} tail"}]},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/bodyPatterns/0/equalToJson"},"title":"Malformed request","detail":"equalToJson operand is not valid JSON: invalid character 'a' looking for beginning of object key string"}]}
```

The same strictness applies to a request *body* at match time, where the
consequence is quieter: a body WireMock would have parsed leniently is a plain
non-match for `equalToJson` and `matchesJsonPath` here. A body that is not JSON
is a fact about the request worth reporting, not a shape to guess at.

### #5 — An unrecognised `${json-unit.*}` placeholder

The documented placeholders — `ignore`, `ignore-element`, `any-string`,
`any-number`, `any-boolean`, `regex` — are interpreted exactly as WireMock
interprets them. Anything else spelled like one is refused:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/j1","bodyPatterns":[{"equalToJson":"{\"a\":\"${json-unit.any-integer}\"}"}]},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/bodyPatterns/0/equalToJson"},"title":"Malformed request","detail":"unknown json-unit placeholder \"${json-unit.any-integer}\""}]}
```

WireMock compares an unrecognised placeholder as literal text, which means the
stub registers and then never matches anything — a typo in a placeholder name
becoming a permanently dead stub.

### #27 — `and` and `or` need two operands

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/ar","bodyPatterns":[{"and":[{"contains":"a"}]}]},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/bodyPatterns/0/and"},"title":"Malformed request","detail":"and needs at least two matchers"}]}
```

WireMock refuses a one-operand combinator too. This is recorded because the
arity is part of the accepted surface, not because the two servers differ.

### #56 — A JSON Schema that could not work is refused

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/s","bodyPatterns":[{"matchesJsonSchema":{"type":"banana"}}]},"response":{"status":200}}'
{"errors":[{"code":1006,"source":{"pointer":"/request/bodyPatterns/0/matchesJsonSchema"},"title":"Invalid JSON Schema","detail":"the schema is not usable: at '/type': value must be one of 'array', 'boolean', 'integer', 'null', 'number', 'object', 'string'"}]}
```

WireMock checks only that the operand is JSON, so each of the documents below
registers there and then behaves in a way nobody writing it intended. A `type`
naming no type and a `$ref` to a location the document does not contain match
nothing, ever. An unrecognised `$schema` poisons the matcher just as silently. A
bare value matches **everything**, which is the same mistake in the direction
that lets traffic through:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/s","bodyPatterns":[{"matchesJsonSchema":42}]},"response":{"status":200}}'
{"errors":[{"code":1006,"source":{"pointer":"/request/bodyPatterns/0/matchesJsonSchema"},"title":"Invalid JSON Schema","detail":"a schema must be a JSON object or a boolean; a bare value here would accept every request"}]}
```

A `schemaVersion` outside the five accepted spellings is refused as well, and
the message names the set so a typo becomes a fix rather than a search:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/s","bodyPatterns":[{"matchesJsonSchema":{"type":"object"},"schemaVersion":"V2020"}]},"response":{"status":200}}'
{"errors":[{"code":1006,"source":{"pointer":"/request/bodyPatterns/0/matchesJsonSchema"},"title":"Invalid JSON Schema","detail":"schemaVersion must be one of V4, V6, V7, V201909, V202012"}]}
```

The same refusal covers a `$ref` that points outside the document. That one
closes no network hole, and it is worth being precise about why: WireMock does
not fetch a remote reference either. A listener that accepted connections and
never answered recorded no connection at all across the whole probe, and a
reference to a schema that was genuinely reachable still did not resolve. What
the refusal converts is a stub that silently matches nothing into a message
naming the field.

There is a cost, and it is the one case where a stub that works on WireMock does
not register here. WireMock's failure is **lazy** — the reference is only
resolved when that subschema is applied — so a bad `$ref` sitting under a
property no request happens to carry never fails there. Compiling the whole
document at registration finds it regardless. In exchange, every schema that
registers is one that can actually decide a request.

`$defs`, `definitions`, JSON pointers, `$anchor` and `$id` all resolve normally;
in-document is the only restriction.

### #49 — A date-time operand or modifier spelling that can never match

The three date-time matchers — `before`, `after` and `equalToDateTime` — take
their expected value as a string, and WireMock parses that string at match time
rather than at registration. A spelling it cannot read is a stub that registers
and then answers nothing, forever, with no diagnostic anywhere. Mockulus parses
the operand when the stub arrives:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/dt1","headers":{"X-When":{"before":"2021-06-14T12:13:14+0300"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/before"},"title":"Malformed request","detail":"\"2021-06-14T12:13:14+0300\" is not a date-time this matcher can compare against; expected an ISO-8601 instant (an offset must be written with a colon), a date, an RFC 1123 date, or a now-relative expression such as \"now +3 days\""}]}

$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/dt2","headers":{"X-When":{"after":"now+2days"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/after"},"title":"Malformed request","detail":"\"now+2days\" is not a date-time this matcher can compare against; expected an ISO-8601 instant (an offset must be written with a colon), a date, an RFC 1123 date, or a now-relative expression such as \"now +3 days\""}]}

$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/dt3","headers":{"X-When":{"after":"  now  "}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/after"},"title":"Malformed request","detail":"\"  now  \" is not a date-time this matcher can compare against; expected an ISO-8601 instant (an offset must be written with a colon), a date, an RFC 1123 date, or a now-relative expression such as \"now +3 days\""}]}
```

Those are three of thirteen spellings WireMock accepts and can never act on: an
offset written without its colon, a time with no date, a bare epoch number, an
empty string, `now+2days` and `now + 2 days` and `now  +2 days`, a value with
trailing text after it, and a keyword padded with spaces. There is no operand
trimming there and no parseability check at registration, so each of them
answers `201` and then misses every request.

The same entry covers the modifier half, because it is the same claim about the
same feature. WireMock drops a key it does not recognise inside a matcher
document without a word: a misspelled `truncateExpected` answers `201`, vanishes
from the mapping the server echoes back, and the truncation the author asked for
never happens.

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/dt7","headers":{"X-When":{"equalToDateTime":"2021-06-14T12:13:14Z","truncateExpectedTo":"FIRST_DAY_OF_MONTH"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/truncateExpectedTo"},"title":"Malformed request","detail":"unknown matcher truncateExpectedTo"}]}
```

Both halves are the same accept-and-ignore failure, and one claim about one
feature: a criterion the author wrote is either honoured or refused, never
quietly discarded. A spelling WireMock can actually act on reproduces it exactly:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/dtok","headers":{"X-When":{"before":"now +2 days"}}},"response":{"status":200}}'
201
```

The accepted operand grammar is in
[SPEC §5.2](../SPEC.md#52-stub-mapping-json--field-support-matrix) and is
WireMock's, not a subset of it: ISO-8601 with a colon-bearing offset, a bare
date, an RFC 1123 date, and the now-relative forms `now`, `now ±N units` and
`±N units`, where the units are plural, run from `seconds` to `years`, and do
not include `weeks`. No configuration key restores the accepting behavior; the
correction is one edit per operand and the `422` names every one of them in a
single response.

### #50 — A truncation parameter that could not take effect

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/tr1","headers":{"X-When":{"equalToDateTime":"2021-06-14T12:13:14Z","truncateExpected":"first day of month"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/equalToDateTime"},"title":"Malformed request","detail":"truncateExpected has no effect on the literal date-time \"2021-06-14T12:13:14Z\"; it applies only to a now-relative expected value such as \"now +3 days\""}]}

$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/tr2","headers":{"X-When":{"before":"now","actualFormat":"dd/MM/yyyy","truncateActual":"first day of month"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/before"},"title":"Malformed request","detail":"truncateActual has no effect when actualFormat reads the value with a pattern; it applies to an ISO-8601 actual carrying an offset, or to unix/epoch"}]}
```

Both keys are wired up in WireMock — an invalid truncation value is a `422`
there — and both are inert on one branch of the matcher without saying so.
`truncateExpected` moves a now-relative expected value and does nothing at all to
a literal one. `truncateActual` truncates an actual that parsed to a zoned
instant and does nothing when `actualFormat` reads the value with a pattern,
including a pattern that carries a time. A stub relying on either combination is
a stub whose author asked for a truncation and got none.

Both are decidable from the stub alone, so both are refused when it registers.
The third inert case is not decidable then, and is mirrored rather than refused:
`truncateActual` is skipped for an ISO actual with no offset, and for a date-only
one, exactly as it is skipped there. That depends on the request rather than the
mapping, and truncating anyway would match where WireMock does not. Against a
stub whose criterion is
`{"equalToDateTime": "2021-06-01T00:00:00", "truncateActual": "first day of month"}`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-14T12:13:14+03:00' "$MOCK/trunc2"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-14T12:13:14' "$MOCK/trunc2"
404
```

The two requests carry the same wall-clock value. The first carries an offset
with it, so it truncates to the first of the month and matches; the second does
not, so it is compared untruncated and misses. That is WireMock's answer to both.

The combinations that can take effect register unchanged:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/tr3","headers":{"X-When":{"after":"now","truncateExpected":"first day of month"}}},"response":{"status":200}}'
201
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/tr5","headers":{"X-When":{"after":"now","actualFormat":"unix","truncateActual":"first day of month"}}},"response":{"status":200}}'
201
```

### #53 — A date-time modifier with no date-time matcher to modify

`actualFormat`, `truncateExpected`, `truncateActual` and `applyTruncationLast`
modify `before`, `after` and `equalToDateTime`. Written anywhere else they are
refused, and the two places they get written are beside a string matcher —

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/af1","headers":{"X-When":{"equalTo":"2021-06-14","actualFormat":"dd/MM/yyyy"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/actualFormat"},"title":"Malformed request","detail":"actualFormat only applies to a before, after or equalToDateTime matcher; on a combinator it must be repeated inside each leaf"}]}
```

— and beside a combinator, at the top of a document whose date-time matchers are
one level down:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/af2","headers":{"X-When":{"and":[{"after":"2000-01-01T00:00:00Z"},{"before":"2030-01-01T00:00:00Z"}],"actualFormat":"dd/MM/yyyy"}}},"response":{"status":200}}'
{"errors":[{"code":10,"source":{"pointer":"/request/headers/X-When/actualFormat"},"title":"Malformed request","detail":"actualFormat only applies to a before, after or equalToDateTime matcher; on a combinator it must be repeated inside each leaf"}]}
```

The other three modifiers are refused in the same two positions, each under its
own name. WireMock accepts all of them in both. Beside `equalTo` or `contains` a
date pattern means nothing, and as a sibling of `and`, `or` or `not` the format
never reaches the leaves that would have used it — so the leaves go on parsing
ISO-8601 and a range written to read `dd/MM/yyyy` reads something else, with
neither the behavior nor a diagnostic for the author who wrote the key once and
expected it to apply.

The message names the fix, and the fix works on both servers:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/af5","headers":{"X-When":{"and":[{"after":"2000-01-01T00:00:00Z","actualFormat":"dd/MM/yyyy"},{"before":"2030-01-01T00:00:00Z","actualFormat":"dd/MM/yyyy"}]}}},"response":{"status":200}}'
201
```

No configuration key restores the accepting behavior for this one or for #50
above. A modifier that reaches nothing is a criterion that does nothing, and the
whole reason it is written is that its author wanted it to.

---

## Matching a request

### #26 — Several keys on one matcher document are a conjunction

`{"contains": "a", "doesNotContain": "b"}` requires both:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/conj" -d 'xax'
200
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/conj" -d 'xaxbx'
404
```

WireMock honours only the first key its binding visits and discards the rest, so
the same document means less there: the stub matches requests its author wrote a
criterion to exclude, with nothing said about it. Conjunction is what a person
writing two criteria intends, and it is the direction that refuses more rather
than less. **A document carrying one key reproduces WireMock exactly.**

### #42 — `matchesJsonPath` over a path that selects an array

The nested form evaluates its inner matcher against the selected node, not
against the node's elements:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/jp" -d '{"tags":["red"]}'
404
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/jp" -d '{"tags":"red"}'
200
```

The stub is `{"expression": "$.tags", "equalTo": "red"}`. WireMock cannot
distinguish a *definite* path that selects an array from a path that returned a
list of hits, so it applies the matcher element-wise and matches the first body.
Mockulus keeps the distinction — it is the whole reason the JSONPath evaluator
is in-repo ([SPEC §6.7](../SPEC.md#67-jsonpath-dialect)). An indefinite path
(`$..tags`, `$.tags[*]`, a filter) returns hits on both servers and the bare
presence form agrees on both; this is the nested form over a definite path only.

### #25 — `ignoreArrayOrder` together with `ignoreExtraElements`

With both flags, an expected array is a subset test, resolved here by maximum
matching. Expected `["${json-unit.any-number}", 2]`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/arr" -d '[5,2,9]'
200
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/arr" -d '[2,5,9]'
200
```

WireMock matches the first and reports the second as a mismatch: its json-unit
search accepts a pairing only when it leaves no actual element unclaimed — a
condition the extra elements it was told to ignore make unsatisfiable — so it
stops backtracking where those elements appear, and the answer depends on the
order of an array the stub declared order-irrelevant.

The difference is **one-directional**: every array WireMock accepts, mockulus
accepts identically, and mockulus additionally accepts some WireMock refuses. A
stub can therefore match here where it missed there, never the reverse — which
matters if your suite asserts on a *non*-match. It is confined to arrays holding
an expected element strictly more permissive than another: a placeholder, or an
object that `ignoreExtraElements` lets another expected object subsume. With
either flag alone, and with literal elements, the two agree exactly.

### #55 — `matchesJsonSchema` validates a document, not the request text

A body that is not JSON is a plain non-match here. WireMock falls back to
validating the raw request text as a JSON *string*, so `not json at all`
satisfies `{"type":"string"}` there, and for a number, boolean or null body it
tries both readings and matches if either succeeds.

The consequence is a matcher that contradicts itself. Against the body `4`,
WireMock matches `{"type":"string"}` — through the raw-text reading — and also
matches `{"not":{"type":"string"}}`, through the parsed one. Both stubs claim
the same request. Here exactly one of them does:

```console
$ curl -s -X POST "$MOCK/any" -H 'Content-Type: application/json' -d '"4"'
a string
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/any" \
    -H 'Content-Type: application/json' -d '4'
404
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/any" -d 'not json at all'
404
```

The stub is `{"matchesJsonSchema": {"type": "string"}}`. WireMock answers `200`
to all three.

Input a matcher cannot read is a fact about the request, which is how every
other matcher here treats it. Trailing content after a complete document is
likewise not a document — deviation #35 applied to this matcher, where WireMock
reads the first value and ignores the rest.

This is confined to scalar and unparseable subjects. A schema is written for an
object or an array, and there the two readings cannot disagree: the raw text of
a request body is never an object, so WireMock's fallback fails the same
`"type": "object"` the parsed document has to satisfy. If your stubs validate
object or array bodies, the two servers agree exactly.

Note also that the difference runs the opposite way from most of this page.
WireMock has two readings to succeed with, so it matches **more** — a suite
moving here can find a scalar-body stub that no longer claims its request.

### #57 — A `$ref` cycle is answered rather than crashed on

A schema whose references form a cycle that consumes no input registers on both
servers. WireMock then returns `500` with a `StackOverflowError` on the first
request that reaches it; `{"$ref":"#"}` is enough, and so are the mutual and
`allOf` forms. Here the cycle compiles and the request gets an answer:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$MOCK/cyc" \
    -H 'Content-Type: application/json' -d '{"a":1}'
404
```

The stub is `{"matchesJsonSchema": {"$ref": "#"}}`, and the `404` is a matcher
that decided rather than a matcher that failed.

Nothing is special-cased to achieve that. A cycle with no escape is
unsatisfiable, so nothing validates against it and the stub matches nothing; a
cycle with an escape branch — the usual recursive-tree shape, where a node is
either a leaf or a list of nodes — resolves through the branch and keeps its
ordinary meaning.

No stub WireMock accepts is refused on this account, so this deviation costs
nothing to migrate. The only difference is that a request answered with a server
error there is answered here.

### #29 — Repeated headers and query parameters are plain any-of

A criterion on a repeated key holds when *any* value satisfies it, including
under `caseInsensitive`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-K: exacx' -H 'X-K: exact' "$MOCK/anyof"
200
```

The stub's criterion is `X-K` `equalTo` `exact`, one of the two values is an
exact hit, and that is enough. WireMock instead picks the value at minimum edit
distance and matches only that one, so a key carrying a near miss alongside an
exact match can answer differently there. Reproducing it would put
a distance computation on the matching hot path for a corner that needs all
three of a multi-valued key, `caseInsensitive`, and a sibling value at non-zero
distance.

A related consequence, which is WireMock's behavior too and is recorded in
[SPEC §6.6](../SPEC.md#66-regex-strategy-java-regex-compatibility) rather than
as a deviation: the negative matchers are also any-of, so they are **not** the
complement of their positive twins. A header carrying `a` and `b` satisfies
`matches("a")` and `doesNotMatch("a")` at the same time.

### #43 — `caseInsensitive` folds by Unicode simple case folding

Java folds per UTF-16 code unit; mockulus uses Unicode simple case folding. The
two disagree in both directions. Against a criterion `equalTo` `i` with
`caseInsensitive`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-F: I' "$MOCK/fold"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -H "$(printf 'X-F: \xc4\xb0')" "$MOCK/fold"
404
```

The second header is `İ` (U+0130, Latin capital I with dot above). Java folds
the Turkish dotted and dotless I to ASCII `i` and `I` and would match it;
mockulus does not. In the other direction mockulus folds supplementary-plane
case pairs that Java never folds. Neither is more correct — they are two
definitions of case folding, and ASCII is unaffected by both.

### #37 — Header values are compared and emitted as UTF-8

Jetty renders header bytes as ISO-8859-1. Against a criterion `equalTo` `café`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H "$(printf 'X-U: caf\xc3\xa9')" "$MOCK/utf8"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -H "$(printf 'X-U: caf\xe9')" "$MOCK/utf8"
404
```

The first request sends the UTF-8 encoding, the second ISO-8859-1; WireMock
answers the opposite way round. Response headers go out under the same
difference. Nothing on the wire declares an encoding for a header value — there
is no parameter to read the way there is for a body's `Content-Type` charset —
so neither is wrong, and the only ones affected are header operands outside
US-ASCII.

### #51 — `equalToDateTime` against a bare date matches that whole day

The stub's criterion is `X-When` `equalToDateTime` `2021-06-14`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-14T13:00:00Z' "$MOCK/day"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-14T23:59:59Z' "$MOCK/day"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-15T00:00:00Z' "$MOCK/day"
404
```

WireMock reads a date-only expected value as midnight, so only the first instant
of 14 June matches there and every other moment of the day the criterion names —
which is to say the whole working day — is a non-match. Nobody writing
`equalToDateTime: "2021-06-14"` means that.

The widening is deliberately confined to equality. `before` and `after` keep
midnight, because widening them would refuse requests WireMock accepts. Against
the same bare date under `after`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-14T13:00:00Z' "$MOCK/after"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'X-When: 2021-06-14T00:00:00Z' "$MOCK/after"
404
```

Midnight itself misses because `before` and `after` are strict on both servers —
an actual exactly equal to the expected satisfies neither, and an inclusive bound
is written `{"or": [{"equalToDateTime": …}, {"before": …}]}`.

So this difference is one-directional: every request WireMock matches, mockulus
matches identically, and no suite that passes there can fail here. A suite
asserting on a *non*-match against a date-only `equalToDateTime` is the one to
look at, and the exact-midnight behavior is one `equalToDateTime:
"2021-06-14T00:00:00"` away.

### #52 — A non-numeric actual under `actualFormat: unix` or `epoch`

The stub's criterion is the query parameter `t`,
`{"before": "2030-01-01T00:00:00Z", "actualFormat": "unix"}`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/epoch?t=1623672794"
200
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/epoch?t=notanumber"
404
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/epoch?t=12.5"
404
```

WireMock answers `500` to the last two. Its parse is an unguarded
`Long.parseLong`, so an empty value, a decimal or any other non-integer escapes
as a `NumberFormatException` and reaches the client as a Jetty error page — from
a header, a query parameter or a cookie alike.

A request that is not what a criterion asked for is a fact about the request, and
that is how every other matcher here treats input it cannot read: an unparseable
body is a non-match for `equalToJson`, an unparseable actual is a non-match for
`before` and `after` without an `actualFormat`, and none of them is an error.
Mock traffic is untrusted input that must not be able to turn a stub into a
server error ([SPEC §17](../SPEC.md#17-security)), so there is no key that
restores the `500`.

The direction is worth knowing if your suite asserts on the status: a request
that produced a `500` there produces a plain unmatched `404` here, with the
usual `text/plain;charset=UTF-8` body.

### #12 — Java-regex constructs RE2 cannot compile

Patterns compile on Go's `regexp` where possible and fall back to a .NET-syntax
engine otherwise, so lookaround, backreferences and possessive quantifiers work:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/look/user"
200
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/look/admin"
404
```

The stub is `urlPathPattern` `/look/(?!admin)[a-z]+`. The fallback engine runs
with a match timeout, `regex_timeout`, default 100 ms: a pathological
backtracking pattern **fails closed** — non-match, plus a
`mockulus_regex_timeouts_total` increment and a WARN log naming the stub —
instead of hanging the request the way an unbounded backtracker would. A pattern
that compiles on neither engine is refused at registration:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" -d '{"request":{"urlPattern":"/a[b"},"response":{"status":200}}'
{"errors":[{"code":1003,"source":{"pointer":"/request/urlPattern"},"title":"Invalid regular expression","detail":"urlPattern does not compile: pattern does not compile on either engine: error parsing regexp: unrecognized escape sequence \\z in `(?s)\\A(?:/a[b)\\z`"}]}
```

The wrapping visible in that message is the compatibility work, not a leak:
patterns are anchored with `\A(?:…)\z` and compiled with DOTALL on and MULTILINE
off, which is what WireMock does.

### #6 — Request bodies are capped at 10 MiB

WireMock reads an unbounded body. Mockulus reads up to `max_body_bytes`:

```console
$ python3 -c "import sys; sys.stdout.write('x'*(10*1024*1024+1))" > big.bin
$ curl -s -X POST "$MOCK/big" --data-binary @big.bin
{"errors":[{"code":1030,"title":"Request body too large","detail":"request body exceeds max_body_bytes"}]}
```

The status is `413`, and it comes back whether or not any stub would have
matched — the cap is applied while the body is being read, before matching runs.
Matching needs the whole body, so the body is read into memory, and a cap is
what stops one client deciding a pod's memory limit. Raise it, or set
`max_body_bytes: 0` for no cap, if you mock an upload endpoint.

---

## Building a response

### #7, #30 — Reason phrases, and what `statusMessage` costs

Go's `net/http` offers no way to choose a reason phrase, and the two halves of
this follow from that one fact.

A response that does **not** set `statusMessage` gets Go's canonical phrase:

```console
$ curl -sD - -o /dev/null "$MOCK/plain222" | head -1
HTTP/1.1 222 status code 222
```

WireMock sends Jetty's table instead — `500` is `Server Error` where Go says
`Internal Server Error`, `222` is `222`, and `420` is `Enhance your Calm`. No
client is known to read the phrase, and HTTP/2 does not carry it at all.

A response that **does** set `statusMessage` gets exactly the phrase it asked
for, written over a hijacked connection — and the connection closes afterwards,
where WireMock keeps it alive:

```console
$ curl -sD - -o /dev/null "$MOCK/msg222" | head -1
HTTP/1.1 222 Totally Fine
$ curl -sD - -o /dev/null "$MOCK/msg222" | grep -i '^Connection'
Connection: close
```

Over HTTP/2 the phrase is not conveyed at all, because the protocol has no
reason phrase. Both effects are scoped to stubs that set the field; every other
response is untouched, which is why the phrase is chosen only when a stub asks
for one. If your suite reuses a connection across a stub that sets
`statusMessage`, expect a reconnect — never a failure, but it will show in a
connection-count assertion.

The phrase itself is encoded once at registration exactly as WireMock encodes
it: CR and LF each become `?`, and a rune outside Latin-1 becomes `?`. It can
neither split the response nor be rejected for something WireMock accepts.

### #38 — A `jsonBody` is served as the tokens it was registered with

Insignificant whitespace between tokens is dropped; nothing else is rewritten.

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/jb"},"response":{"status":200,"jsonBody":{"n":1e2,"s":"café"}}}' >/dev/null
$ curl -s "$MOCK/jb"
{"n":1e2,"s":"café"}
```

WireMock re-serializes the document: `1e2` is emitted as `100`, a `\u` escape is
emitted as the character it names, and the hex digits of an escape it does keep
are normalised to upper case. The documents are structurally equal either way —
which is what the differential harness compares — and only the bytes differ. It
matters if your assertion is a byte comparison on the response.

### #44 — A one-element header array stays an array in the document

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/hdr"},"response":{"status":200,"headers":{"X-One":["a"]},"body":"x"}}'
{"id":"e29b1535-3564-487c-a750-5283455e751e","request":{"url":"/hdr"},"response":{"status":200,"headers":{"X-One":["a"]},"body":"x"},"uuid":"e29b1535-3564-487c-a750-5283455e751e"}

$ curl -sD - -o /dev/null "$MOCK/hdr" | grep -i '^X-One'
X-One: a
```

WireMock collapses the array to a bare string in the stored mapping. Serving
agrees — one header line either way — so this is the echoed document only.

### #48 — A response declaring `Content-Type` more than once

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/ct1"},"response":{"status":200,"headers":{"Content-Type":["application/json","text/plain"]},"body":"{}"}}'
{"errors":[{"code":10,"source":{"pointer":"/response/headers/Content-Type"},"title":"Malformed request","detail":"Content-Type takes a single media type, and this response declares 2"}]}
```

The other spelling of the same mistake is two keys whose names differ only in
case, which HTTP says are one header:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/ct2"},"response":{"status":200,"headers":{"Content-Type":"application/json","content-type":"text/plain"},"body":"{}"}}'
{"errors":[{"code":10,"source":{"pointer":"/response/headers/Content-Type"},"title":"Malformed request","detail":"Content-Type takes a single media type, and this response declares 2"}]}
```

WireMock accepts both, takes the last value and appends a charset the stub never
named, so neither of the two values the author wrote is what goes out. A response
carries exactly one Content-Type and its value is one media type; two of them
describe a message that cannot be sent, no reading of the pair is more likely to
be what was meant than any other, and refusing is the only answer that does not
hand back a header its author did not write.

One value is unaffected in either spelling, and every other header may repeat
freely — the stub at `/ct5` sets `X-Multi` to `["a","b"]`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/ct4"},"response":{"status":200,"headers":{"Content-Type":["application/json"]},"body":"{}"}}'
201
$ curl -sD - -o /dev/null "$MOCK/ct4" | grep -i '^Content-Type'
Content-Type: application/json
$ curl -sD - -o /dev/null "$MOCK/ct5" | grep -i '^X-Multi'
X-Multi: a
X-Multi: b
```

There is no configuration key for this one. The fix is to write the media type
you meant, once, and to include the charset yourself if you want one — nothing
is appended to a Content-Type here.

### #15 — Fault injection is byte-faithful on HTTP/1.1 only

The four faults — `CONNECTION_RESET_BY_PEER`, `EMPTY_RESPONSE`,
`MALFORMED_RESPONSE_CHUNK`, `RANDOM_DATA_THEN_CLOSE` — are produced by hijacking
the connection, which HTTP/2 does not permit. Over HTTP/2 they degrade to a
stream reset, which is a different thing on the wire and a different thing for
the client library under test.

This is why cleartext HTTP/2 is off by default: `h2c_enabled` is `false`. Turn
it on only if you know no stub in the deployment uses `fault`. Over TLS, full
HTTP/2 is available and the same caveat applies.

---

## Templating

### #13 — Parse errors at registration, render errors in the body

WireMock defers every template error to serve time. Mockulus splits them: a
template that does not parse, or names a helper outside the allowlist, is `422`
at registration —

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/h"},"response":{"body":"{{jwt \"x\"}}","transformers":["response-template"]}}'
{"errors":[{"code":1002,"source":{"pointer":"/response/body"},"title":"Template error","detail":"unknown helper \"jwt\"; mockulus supports base64, concat, default, join, jsonPath, lookup, lower, lowercase, math, now, number, pickRandom, randomDecimal, randomInt, randomValue, range, replace, size, split, substring, trim, upper, uppercase, urlEncode"}]}
```

— while an error that can only happen against a real request renders into the
response body, as WireMock does:

```console
$ curl -sD - -X POST "$MOCK/render" -d 'not json' | head -1
HTTP/1.1 500 Internal Server Error
$ curl -s -X POST "$MOCK/render" -d 'not json'
Template render error: jsonPath: the document is not valid JSON
```

Serve-time render errors are counted by `mockulus_template_render_errors_total`.
The helpers excluded from the allowlist are excluded deliberately: `file`,
`systemValue`, `secret`, `hostname` and the XML helpers give a stub filesystem,
environment or network reach, and templates here are sandboxed by construction
rather than by configuration ([SPEC §17](../SPEC.md#17-security)).

### #45 — `math` with `/` keeps the fraction

```console
$ curl -s "$MOCK/math"
2.5
```

The body is `{{math 10 "/" 4}}`. WireMock rounds half-up to an integer when both
operands are integral and renders `3`. Discarding the fraction of a division a
template asked for is a surprising default, and the rounded value is one more
`{{math}}` away for anyone who wants it.

### #39 — A helper that finds nothing renders nothing

`lookup` with a key absent from its subject, and `substring` over an empty body,
both produce the empty string:

```console
$ curl -s "$MOCK/sub"
[]
```

The body is `[{{substring request.body 0 3}}]` and the request had no body.
WireMock renders the whole subject's `toString` for the first case (`{tier=gold}`
lands in your response) and `0` for the second. Rendering an internal
representation into a response body is not an outcome worth reproducing.

### #46 — `base64Body` is templated

When a stub asks for response templating, the base64 is decoded and the result
is then rendered:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/b64"},"response":{"status":200,"base64Body":"e3tyZXF1ZXN0LnVybH19","transformers":["response-template"]}}' >/dev/null
$ curl -s "$MOCK/b64"
/b64
```

The encoded payload is `{{request.url}}`. WireMock templates an inline `body`
and a `jsonBody` but never a `base64Body`, so a payload encoded specifically to
keep it away from the template engine renders here and stays literal there. If
that is what your stub was doing, use `disableBodyTemplating` below.

### #31 — `disableBodyTemplating` is an extension, not parity

```console
$ curl -s "$MOCK/tmpl-off"
{{request.url}}
```

The stub sets `"transformerParameters": {"disableBodyTemplating": true}` and its
body is left alone. WireMock has no such parameter and templates an inline body
either way; its own `disableBodyFileTemplating` guards the `bodyFileName` path
only. The extension earns its place — a payload that is itself a Handlebars
template is exactly the body a stub wants exempted — but a stub carrying it
renders differently on the two servers, so it is a deviation, not a fix. A value
that is not a boolean is refused rather than ignored:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" \
    -d '{"request":{"url":"/tmpl-bad"},"response":{"status":200,"body":"x","transformers":["response-template"],"transformerParameters":{"disableBodyTemplating":"yes"}}}'
{"errors":[{"code":10,"source":{"pointer":"/response/transformerParameters/disableBodyTemplating"},"title":"Malformed request","detail":"disableBodyTemplating takes a boolean"}]}
```

---

## Driving the admin API

### #41 — The echoed mapping is the document you registered

Mockulus stores and returns what you sent. WireMock fills in defaults on the way
out: an absent `response` becomes `{"status": 200}`, a `response` without
`status` gains one, and an absent `request` becomes `{"method": "ANY"}`.

```console
$ curl -s -X POST "$ADMIN/__admin/mappings" -d '{"request":{"urlPath":"/minimal"}}'
{"id":"5910a075-4300-4099-bf52-b0df11682353","request":{"urlPath":"/minimal"},"uuid":"5910a075-4300-4099-bf52-b0df11682353"}
```

Serving agrees in all three cases — that stub answers `200` with an empty body,
as it would there. Only the document differs. Together with #44 above, this is
the one to know about if your suite round-trips a mapping through `GET
/__admin/mappings/{id}` and compares it to the file it sent.

### #4 — `mappings/save` marks everything persistent

WireMock writes the in-memory stubs to its `mappings/` directory. There is no
filesystem to write to in a Couchbase-backed cluster, so the closest honest
equivalent is what the endpoint does here: set `persistent: true` on every
current stub, which removes their TTL and makes them survive
`POST /__admin/mappings/reset`.

```console
$ curl -s "$ADMIN/__admin/mappings" | python3 -c 'import json,sys; print(json.load(sys.stdin)["meta"]["total"])'
22
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings/save"
200
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings/reset"
200
$ curl -s "$ADMIN/__admin/mappings" | python3 -c 'import json,sys; print(json.load(sys.stdin)["meta"]["total"])'
22
```

The reset removed nothing, because `save` had made every stub persistent.
That is the deviation in one transcript: after `save`, `mappings/reset` is a
no-op rather than a clean slate.

### #40 — `importOptions` present without `duplicatePolicy`

An `importOptions` object that does not name a policy takes the documented
default, `OVERWRITE`:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings/import" \
    -d '{"mappings":[{"id":"33333333-3333-3333-3333-333333333333","request":{"url":"/imp"},"response":{"status":201}}]}'
200
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST "$ADMIN/__admin/mappings/import" \
    -d '{"mappings":[{"id":"33333333-3333-3333-3333-333333333333","request":{"url":"/imp"},"response":{"status":202}}],"importOptions":{}}'
200
$ curl -s -o /dev/null -w '%{http_code}\n' "$MOCK/imp"
202
```

WireMock treats the absent policy as `IGNORE` and keeps the existing stub, so
the same two calls would leave `201` there. When the whole `importOptions`
object is omitted the two agree on `OVERWRITE` — the divergence is the
partially-filled object only.

### #21 — Malformed admin payloads get an error envelope, and imports are atomic

A `null` `mappings` array on import, or a missing `deleteAllNotInImport`, raises
an unhandled `500` in WireMock. Here it is the standard envelope:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings/import" -d '{"mappings":null}'
{"errors":[{"code":10,"source":{"pointer":"/mappings"},"title":"Malformed request","detail":"import needs a mappings array"}]}
```

The status is `422`. The second half of this deviation matters more in practice:
an import here is validated in full before anything is written, so a batch with
one bad mapping changes nothing. WireMock's applies partially, leaving you to
work out which half landed.

### #20 — `find-by-metadata` and `remove-by-metadata` consider only tagged stubs

A stub with an explicit `"metadata": null` counts as having no metadata:

```console
$ curl -s -X POST "$ADMIN/__admin/mappings/find-by-metadata" -d '{"matchesJsonPath":"$.suite"}'
{"mappings":[{"id":"bcd960c3-54aa-404f-a116-ac8da36e8e50","metadata":{"suite":"run-7"},"request":{"url":"/m1"},"response":{"status":200},"uuid":"bcd960c3-54aa-404f-a116-ac8da36e8e50"}],"meta":{"total":1}}
```

A stub registered with `"metadata": null` is absent from that result. WireMock
serializes absent metadata to the literal `null` and matches *against* it, so a
broad matcher there can remove every untagged stub in the deployment. In a
shared deployment where `remove-by-metadata` is the documented cleanup path for
a CI run, that is one runner deleting another runner's stubs.

`remove-by-metadata` additionally returns the removed mappings under the standard
list envelope where WireMock returns `{}`. That is a catalogued extension —
additive fields on either side are ignored by the differential diff — not a
behavioral difference.

### #32 — `GET /__admin/scenarios` does not embed member stubs

```console
$ curl -s "$ADMIN/__admin/scenarios"
{"scenarios":[{"id":"checkout","name":"checkout","state":"Started","possibleStates":["Started","paid"]}]}
```

WireMock additionally embeds every member stub under `mappings`. A scenario
holding a hundred stubs would repeat all hundred inside a listing whose caller
wants a state name, and the same documents are one `GET /__admin/mappings` away.

### #33, #34 — Setting a scenario state

An unsupported state is refused with `400` and code `1031`; WireMock answers
`422` with its code `11`. Both refuse the write and both name the scenario and
the state, so the failure is loud either way.

```console
$ curl -s -X PUT "$ADMIN/__admin/scenarios/checkout/state" -d '{"state":"nope"}'
{"errors":[{"code":1031,"source":{"pointer":"/state"},"title":"Invalid scenario","detail":"no stub in scenario checkout uses the state nope"}]}
```

`Started` is a possible state of every scenario, so setting it always succeeds:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X PUT "$ADMIN/__admin/scenarios/checkout/state" -d '{"state":"Started"}'
200
```

WireMock derives `possibleStates` from the stubs alone and refuses to set
`Started` when no stub names it — even though it is the state every scenario is
in until something moves it, and the state `POST /__admin/scenarios/reset`
returns it to. Ours keeps the listing, the request path and the two ways of
going back to the beginning agreeing with each other.

### #28 — Near-miss ordering is ours

Near-miss output lists the stubs closest to an unmatched request, ordered by a
distance mockulus defines ([SPEC §6.8](../SPEC.md#68-near-miss-scoring)).
WireMock's ordering is its own and is not reproduced: the ranking is a debugging
aid outside the strict-compatibility surface, and no matching decision depends
on it. The endpoints, their shapes and their statuses are compatible; the order
of the list inside them is not pinned.

---

## Operating a deployment

The deviations in this section are consequences of running N replicas against a
shared store instead of one JVM holding everything in memory. None of them are
tunable away entirely; all of them are bounded, and the bound is a config key.

### #10 — The journal is eventually consistent

WireMock's `verify()` reads a list held by the same process that served the
request, so it is immediate. Here, entries are batched to Couchbase and become
visible to verification within `journal_flush_interval` (default 200 ms) plus
indexing latency — typically under 500 ms in total.

**What to do.** Use the polling and timeout forms your WireMock client already
provides (`verify(...)` with a timeout, or the `await`-style helpers). A bare
verify immediately after the last request is the one call that can flake. The
E2E corpus uses two-second windows.

### #11 — Stub propagation across replicas takes up to a second

An admin write is reflected immediately on the replica that handled it — that
pod splices the compiled stub into its snapshot before it answers `201`, so a
single-pod `stub → call → verify` flow sees zero staleness. Other pods converge
within `sync_interval`, default 1 s.

**What to do.** With more than one replica behind a Service, a test that
registers a stub and immediately calls the mock port can land on a pod that has
not seen it yet. Either retry the first call, or give the suite its own instance
(see the isolation guidance in [SPEC §1](../SPEC.md#1-purpose--product-definition)).

### #17 — An expired stub can outlive its TTL on a pod

A `persistent: false` stub whose TTL expires naturally may keep matching on pods
that already hold it, for up to `resync_interval` (default 5 minutes). Expiry
does not bump the epoch that drives propagation, so pods only notice on the next
unconditional full reload.

Explicit deletes and resets are not affected: those bump the epoch and propagate
within `sync_interval`, as #11 describes. This is the natural-expiry path only,
and it is a stub that lives slightly too long — never one that disappears early.

### #16 — Journal-backed verification has bounds

Two caps, both config keys:

- `journal_query_scan_limit` (default 10 000): a criteria query — `count`,
  `find`, `remove` — scans the newest N entries. Counts beyond that window
  under-report.
- `journal_max_body` (default 64 KiB): stored bodies are capped, and truncation
  is flagged on the entry with `"bodyTruncated": true`. A body criterion that
  would have matched past the cap under-reports too.

Functional suites stay well inside both. Load tests keep the journal off
entirely, which is why it is off by default (#1). The journal serves verification,
not analytics.

### #8 — `POST /__admin/shutdown` is disabled by default

```console
$ curl -s -X POST "$ADMIN/__admin/shutdown"
{"errors":[{"code":1001,"title":"Unsupported endpoint","detail":"/__admin/shutdown is not supported in mockulus v1 — see ROADMAP.md"}]}
```

Kubernetes owns the lifecycle of a pod; an endpoint that lets any client on the
mock port end a replica is not something a shared deployment wants reachable.
Set `admin_shutdown_enabled: true` to enable it, after which the endpoint
answers `200` and the process runs its normal drain and shutdown sequence
(`shutdown_drain`, then `shutdown_timeout`). Note that the disabled response
reuses the unsupported-endpoint code `1001`, so it reads as though the endpoint
does not exist rather than as though it is switched off; the flag is the
difference.

### #9 — The admin API is also on a dedicated port, and can leave the mock port

WireMock serves `/__admin` on its single port. Mockulus serves it on both the
mock port (8080, for client-library compatibility) and a separate admin port
(9090), which additionally carries `/healthz`, `/readyz`, `/metrics` and
`/debug/pprof`.

Setting `admin_on_mock_port: false` removes it from the mock port. The path then
falls through to the match engine like any other, which means it answers with
the unmatched 404, in plain text:

```console
$ curl -sD - "$MOCK/__admin/mappings" | head -2
HTTP/1.1 404 Not Found
Content-Type: text/plain;charset=UTF-8
$ curl -s -o /dev/null -w '%{http_code}\n' "$ADMIN/__admin/mappings"
200
```

That is the hardened posture for a deployment whose mock port is exposed beyond
its namespace: the admin API stays on a port your ingress does not publish. It
breaks WireMock client libraries pointed at the mock port, so point them at the
admin port instead.

---

## How this list is maintained

Compatibility here is established differentially, not by reading WireMock's
documentation. The same operations run against the pinned image and against
mockulus, and the responses are diffed with normalization rules — `Date`,
`Server` and connection headers ignored, JSON bodies compared structurally.
Anything that differs is either a deviation on this page or a bug, and CI fails
on the second ([SPEC §5.6](../SPEC.md#56-differential-compatibility-verification-the-compat-tiebreaker)).

Two consequences are worth stating for anyone planning around this page.

Removing a deviation changes the behavior of a stub that registers successfully
today, so after v1.0 it can only happen in a major version. Turning a `422` into
a supported feature does not, which is why that is the one transition a minor
release is allowed to make — every item in [ROADMAP.md](../ROADMAP.md) is that
transition waiting for demand, and the 422 codes are counted by
`mockulus_admin_requests_total` so the demand is measurable rather than argued.

And the list does not grow quietly. A change in behavior has to update the
behavior catalog and the E2E corpus in the same pull request, and CI fails the
build when a spec table row has no catalog entry or a catalog behavior has no
passing case. A new deviation therefore arrives as a numbered entry in SPEC
§5.5, a corpus case that pins it, and a section here — or it does not arrive.
