# Open decisions

Questions that are genuinely open — where the implementation has taken a
position that is defensible but reversible, and someone should confirm it before
v1.0. Each entry records what was chosen, why, what it would cost to change, and
what evidence would settle it.

Settled decisions live in the ADR (SPEC §3) and the deviation list (SPEC §5.5).
This file is the waiting room, not the record.

---

## D-OPEN-1 — Multi-value selection under `caseInsensitive`

**Status:** implemented as plain any-of · **Owner:** M6 · **Reversible:** yes

When a repeated header or query parameter meets an `equalTo` matcher with
`caseInsensitive: true`, WireMock appears to select the **minimum-distance**
value rather than simply accepting any value that matches. The evidence is
single-agent but internally corroborated.

Mockulus implements plain any-of: the criterion is satisfied when at least one
value matches. The divergent corner needs all three of a multi-valued key,
`caseInsensitive`, and a near-miss sibling at non-zero distance — and closing it
would put a distance computation on the matching hot path, against SPEC §16.3
rule 1.

**To change:** `matchAnyValue` in `internal/matchers/matchers.go` would take a
distance function and return an argmin rather than a boolean.

**To settle:** re-probe with `contains` + `caseInsensitive`, and with a second
exact-match-at-non-zero-distance matcher, to establish whether the affected class
is exactly `{equalTo + caseInsensitive}`.

If it stands, it becomes a numbered deviation in §5.5.

---

## D-OPEN-2 — `hasExactly` semantics

**Status:** rejected with 422, deferred to the roadmap · **Owner:** roadmap 1.5

Probing showed `hasExactly` is **size-equality plus each-pattern-matches-some-value**,
not the multiset bijection the name suggests: `hasExactly[{equalTo:"a"},{equalTo:"a"}]`
matches `?x=a&x=zzz`.

This only gates the roadmap item — v1 rejects the matcher outright (SPEC §5.2) —
but the finding is recorded here so whoever implements it does not have to
rediscover it, and does not implement the intuitive-but-wrong version.

---

## D-OPEN-3 — Near-miss distance weighting

**Status:** unresolved, still `[DH]` in SPEC §6.8 · **Owner:** M5

Two independent probes established only one ordering fact: an HTTP-method
mismatch outranks a one-character URL difference. Both explicitly warned against
extrapolating a formula from that.

SPEC §6.8 already sets the bar at "helpful and stable, not bit-identical", since
diagnostic output is outside the strict-compat surface. So this may never need a
precise answer — but M5 should decide deliberately rather than by default.

**To settle:** a systematic sweep — hold every criterion fixed but one, vary the
edit distance, and find the crossover per criterion.

---

## D-OPEN-4 — Response framing

**Status:** Go's default (Content-Length on small bodies) · **Owner:** M6

WireMock always uses `Transfer-Encoding: chunked` and never sets
`Content-Length`, at every body size. Go sets `Content-Length` for bodies it can
buffer. The difference is invisible to any HTTP client and the differential diff
ignores connection headers, so this is recorded rather than fixed.

It would matter only to a test asserting on framing itself, which would be
asserting on something neither server promises.

---

## D-OPEN-5 — Reason phrase when `statusMessage` is absent

**Status:** Go's phrase table · **Owner:** M6 · **Reversible:** at a cost

The larger question this entry used to carry — whether `statusMessage` reaches
the wire at all — is closed. It does. A stub that sets it is served over a
hijacked connection with the status line written by hand, which is the only way
to choose a reason phrase in Go, and the phrase matches the pinned WireMock byte
for byte including its quirks (CR and LF each become `?`, a rune outside
Latin-1 becomes `?`). The cost is that such a response closes the connection
where WireMock keeps it alive; that is now numbered deviation #7 rather than an
open question, because it follows from hijacking and hijacking is the only
mechanism available.

What stays open is the *other* phrase — the one sent when a stub says nothing.
Mockulus sends Go's (`500` → `Internal Server Error`, `222` → `status code
222`); WireMock sends Jetty's (`500` → `Server Error`, `222` → `222`, `420` →
`Enhance your Calm`). An explicitly empty `statusMessage` is the same question
seen from the other side: WireMock emits an empty phrase there, mockulus emits
the canonical one, because an empty string is how "unset" is spelled in
`CompiledResponse`.

Closing it is not a small change dressed as a table lookup. Every response would
have to take the hijacked path to control its phrase, which is exactly the
per-stub opt-in that keeps the current implementation within P2, and it would
put deviation #7's connection-close on all traffic.

**To settle:** find a client that reads the reason phrase and acts on it. Nobody
has produced one; HTTP/2 has no phrase at all, which is the strongest evidence
that none exists. If none does, this becomes a numbered deviation and stops
being a question.

---

## D-OPEN-6 — Sibling matcher keys on one document

**Status:** implemented as a conjunction · **Owner:** M6 · **Reversible:** yes

`{"contains": "a", "doesNotContain": "b"}` is one matcher document carrying two
matcher keys. Mockulus treats it as a **conjunction** — both must hold — and a
code comment asserts that WireMock does the same. Probing during the M1
compatibility pass contradicted that comment: pinned WireMock 3.13.2 appears to
honour only the **first** key it deserializes and discard the rest.

The conjunction is the more useful reading and the safer one: a stub written
with two criteria almost certainly means both, and silently dropping one is how
a test passes against a request it should have rejected. But it is a divergence,
and right now it is an undocumented one resting on a comment that is wrong.

**To change:** `Compile` in `internal/matchers/compile.go` collects every
recognised key into `built`; honouring only the first would mean stopping after
one, or rejecting the multi-key form outright.

**To settle:** establish WireMock's key ordering — whether "first" means source
order, or the order its Jackson binding happens to visit — because a behavior
that depends on JSON member order is one no client can rely on either, which
would argue for the third option: **reject the multi-key form at registration**
(P3) rather than pick a winner.

Whichever way it lands, it becomes a numbered deviation in §5.5 or a corrected
comment.

---

## D-OPEN-7 — `and` / `or` with a single operand

**Status:** accepted; WireMock rejects · **Owner:** M6 · **Reversible:** yes

WireMock requires `and` and `or` to carry at least two operands and answers 422
code 10 for a one-operand form. Mockulus accepts it, and a one-operand `and` is
simply that operand.

Accepting it is harmless in itself, but it breaks the D2 contract in the
direction that matters least and is still worth noticing: a stub that registers
here and 422s there is a mappings file that cannot be migrated back to WireMock.

**To change:** an arity check in `compileCombinator`, one condition and a test.

---

## D-OPEN-8 — `\s` and `\S` on U+000B

**Status:** translated to Java's definition · **Owner:** M6 · **Reversible:** yes

Java's `\s` is `[ \t\n\x0B\f\r]`; RE2's omits the vertical tab, U+000B. The Java
syntax translation added in M1 lowers `\s` and `\S` to Java's definition, so a
pattern containing the most common escape in the language now means the same
thing on both servers.

This contradicts SPEC §6.6 item 4, which says RE2-vs-Java divergences are
**accepted** for patterns that compile on both engines. The translation was
included anyway because the fix is exact, costs nothing at match time, and
removes a wrong answer rather than a missing feature — but the carve-out it
overrides was a deliberate decision, so overriding it should be a deliberate one
too.

**To change:** delete the `s` row from `javaLetterClasses` in
`internal/regexx/javasyntax.go`. One table entry.

**To settle:** decide whether §6.6 item 4 still describes the intended policy now
that a translation step exists. If translation is the policy, item 4 should say
so and name what is still accepted as divergent.

---

## D-OPEN-9 — `statusMessage: ""`

**Status:** sends the canonical phrase · **Owner:** M6 · **Reversible:** yes

A stub setting `"statusMessage": ""` explicitly asks for an empty reason phrase.
Mockulus currently treats empty as absent and sends the canonical phrase for the
status code instead, because the hijacking path that writes a custom reason
phrase is only entered when the field is non-empty (§5.2).

An explicitly empty string is a different statement from an omitted field, and a
test written to assert a blank reason phrase would get one it did not ask for.
Against that: HTTP/1.1 permits an empty reason phrase but little tooling expects
it, and entering the hijack path for it costs a connection close.

**To change:** distinguish "absent" from "present and empty" when decoding
`statusMessage` in `internal/stub/response.go`, and let the empty case take the
hijack path.

**To settle:** probe pinned WireMock with `"statusMessage": ""` and match it.

---

## D-OPEN-10 — Cross-pod read consistency on the rebuild path

**Status:** same-pod writes are consistent, peer writes are not · **Owner:** M6 ·
**Reversible:** yes

This one was measured, not reasoned about, and it is the most consequential
entry in this file.

A KV range scan answers from the vbucket's **persisted** view. A mapping the
cluster has acknowledged from memory is therefore absent from the scan until the
disk queue catches up — and the scan reports success, so `LoadAll` returns a
short answer with no error and the builder installs it. Under eight-way write
pressure, 27 of 40 scans missed a document that had just been written; in the
shape a corpus case hit, 3 of 30 reloads returned **zero rows and no error**
against a keyspace holding one stub, which installs an empty snapshot over a
populated one.

The driver now carries this pod's own mutation tokens into the read
(`ConsistentWith`), which closes it for writes this pod made: 0 of 40 and 0 of
30 on the same probes.

**What is still open is the other pod's write.** `LoadAll` reads the epoch
*before* the scan and stamps the snapshot with it, so a pod rebuilding because a
peer bumped the epoch can install a view predating that peer's write, stamp it
with the new epoch, and read as converged. The stub is then missing on that pod
until `resync_interval` (5 min) rather than `sync_interval` (1 s). Measured on a
second store handle: 2 misses in about 60 immediate reads, widest window 117 ms.
This is a T3 (multi-pod) property; single-pod deployments are unaffected.

It matters because §8's propagation bound is the thing the whole architecture is
sold on, and 5 minutes is not 1 second.

**Options:**

- **(a) Accept and document.** State the real worst case in SPEC §8: a remote
  write converges within `resync_interval`, not `sync_interval`. Costs nothing,
  and makes the spec true — but it weakens the headline guarantee.
- **(b) Publish the causing write's mutation token.** Have `BumpEpoch` record
  the vbucket / partition-uuid / seqno in a meta document, so any pod folds it
  into its own scan requirement. One extra KV upsert per admin write and one
  extra KV get per reload; cross-pod reads become as strong as same-pod ones,
  and the `sync_interval` bound holds as written. **Recommended.**
- **(c) Always bulk-load through N1QL at `RequestPlus`.** Cross-pod consistent
  by construction, but it costs the primary index and the scan throughput the
  range-scan path exists for (§7.2).

**To settle:** (b) is a contained change to `internal/store/couchbase` and the
epoch document's shape. The probe that measures it is the second-handle
immediate-read loop; whatever lands should be checked against it rather than
argued.

---

## D-OPEN-11 — `deleteWhere` still backs the journal

**Status:** mappings and scenarios fixed, one caller left · **Owner:** M5 ·
**Reversible:** no reason not to

The `DELETE FROM` statement behind bulk removal is planned as a KV sequential
scan of the same persisted view as D-OPEN-10, so a document written milliseconds
earlier is invisible to it and is **never deleted** — while the caller is told
200. Measured 7 times in 20 against an idle single node. Unlike the read case
this is permanent rather than transient: the document really exists, so every
later reload keeps serving it.

For mappings this is fixed: `DELETE /__admin/mappings` and
`POST /__admin/mappings/reset` now select keys with the watermarked bulk read
and remove by key, which also gives each removal a mutation token for the reload
that follows. 7 in 20 became 0 in 20.

`DeleteAllScenarios` took the same treatment at M4, which is where it mattered
most: a scenario state document is written by the request that transitions the
flow, so the reset a suite makes between tests is the call most likely to land
inside the window, and what it leaves behind is a flow that resumes from the
middle. `scenario-reset-001` drives a transition and resets with nothing in
between, and asserts the read-back with no polling window at all.

`ClearJournal` still calls the old path. The journal collection is not read by a
bulk scan, so a surviving entry cannot resurrect a stub — the blast radius is a
`DELETE /__admin/requests` that silently does not clear.

**To change:** the same `removeKeys` treatment, applied when M5 lands.

---

## D-OPEN-12 — `disableBodyTemplating` is ours, not WireMock's

**Status:** implemented as SPEC §10.1 describes · **Owner:** M6 ·
**Reversible:** yes

SPEC §10.1 carries `transformerParameters: {"disableBodyTemplating": true}` as a
per-stub opt-out "honored **[DH]**". Probed against pinned 3.13.2, the answer to
that [DH] is that there is nothing to honor: the parameter does not exist there.
`ResponseTemplateTransformer` reads exactly one parameter,
`disableBodyFileTemplating`, and that one guards only the `bodyFileName` path —
an inline body is templated either way. Four spellings were sent
(`disableBodyTemplating`, `disableBodyParsing`, `disableTemplating`,
`bodyTemplating`) and all four rendered.

So a stub document carrying the parameter answers differently on the two
servers: WireMock renders the body, mockulus serves it literally and templates
the headers around it. `templating-disable-body-001` pins what mockulus does and
says why it cannot be `wm: verified`.

Two questions, and they are separable:

- Keep the extension, rename it to WireMock's `disableBodyFileTemplating` and
  scope it to body files, or accept both spellings. The extension earns its
  place — a payload that is itself a Handlebars template, or an Angular fixture,
  is exactly the body a stub wants to exempt — but it is currently an extension
  wearing a name that reads like parity.
- A value that is not a boolean is ignored in silence. `{"disableBodyTemplating":
  "true"}` templates the body, which is the accept-and-behave-differently P3
  exists to prevent, and the same shape deviation #23 rejects for
  `{"absent": false}`. A 422 naming the parameter would be consistent with it.

**To change:** `compileTemplates` in `internal/stub/response.go` for both halves.

**To settle:** the first is a product call, not a probe. If the extension stands
it wants a number in §5.5, since a WireMock user's stub silently changes meaning
when it moves here.

---

## D-OPEN-13 — The scenarios admin surface diverges three ways

**Status:** implemented as SPEC §5.1 describes · **Owner:** M6 ·
**Reversible:** yes

§5.1 marks both scenario admin rows **[DH]** — the listing's shape and the
PUT-state validation. Probed against pinned 3.13.2, all three answers differ
from ours, and each one is a separate call:

- **The listing carries a fourth field.** WireMock puts the scenario's member
  stubs under `mappings`, in full. §5.1 names `id`, `name`, `state` and
  `possibleStates`, and ours stops there. The field is not free: a scenario with
  a hundred stubs in it repeats all hundred inside a listing whose caller
  usually wants a state name, and the same documents are one `GET
  /__admin/mappings` away. It is also the reason the scenario cases are `wm:
  n/a` — the two documents differ by more than identity, so a differential diff
  would report it every run.
- **An unsupported target state is 422 code 11 there and 400 code 1031 here.**
  Both refuse the write and both name the scenario and the state, so the failure
  is loud either way; only the status and the code differ. Ours is what §5.1 and
  Appendix B say, and 400 is the better reading of a path parameter naming a
  state that does not exist — but a client that branches on the status sees a
  difference.
- **`Started` is always settable here and not always there.** WireMock derives
  `possibleStates` from the stubs alone, so a scenario whose stubs name only
  `alpha` and `beta` reports exactly those two and refuses `PUT {"state":
  "Started"}` with the same 422 — even though `Started` is the state the
  scenario is *in* until something moves it, and `POST /__admin/scenarios/reset`
  puts it back there. Ours adds `Started` to every scenario's possible states,
  so the listing agrees with what the request path will do and the two ways of
  going back to the beginning agree with each other.

**To change:** `recordScenario` in `internal/match/snapshot.go` for the third,
`setScenarioState` in `internal/admin/scenarios.go` for the second, and
`scenarioView` for the first.

**To settle:** the first two are product calls rather than probes — the
measurements are in, and what is open is whether parity is worth the cost. The
third is the one that should not change: a scenario that cannot be put into the
state its own reset produces is a WireMock bug wearing a validation message. If
any of them stands it wants a number in §5.5, since a suite written against
WireMock can tell the difference.

---

## D-OPEN-13 — JSONPath body matching allocates on the request path

**Status:** measured, not fixed · **Owner:** post-v1 · **Reversible:** yes

The M6 benchmark pass put numbers on SPEC §16.3 rule 1, and one line stands out:

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `Match/mixed/1000/exact` | 993 | 0 | 0 |
| `Match/mixed/1000/regex` | 1,048 | 0 | 0 |
| `Match/mixed/1000/jsonpath` | 1,483 | **1,152** | **29** |

Every other matcher shape is allocation-free per request. A `matchesJsonPath`
body criterion decodes the body into `map[string]any`, and that decode is the
1,152 bytes: 29 allocations for one request, on the path §16.3 says must not
allocate what it can avoid.

It is bounded and it is pay-per-use — a deployment with no JSONPath body
matchers pays none of it — so it is not urgent. But it is the one measured
violation of a rule the rest of the code holds to strictly, and at S2's shape it
is the difference between the JSONPath row and the other two.

**To change:** the decode is already cached per request in
`ParsedRequest.bodySubject`, so several JSONPath matchers on one request share
it. What would remove the rest is evaluating the path against the raw bytes
rather than a decoded tree — a streaming evaluator over `encoding/json`'s
scanner, or a decode into a reusable arena held by the pooled request.

**To settle:** decide whether the S2 budget needs it. The benchmark that
measures it is `BenchmarkMatch/mixed/1000/jsonpath`, and `test/load/BASELINE.md`
records the number to beat.

---

## D-OPEN-14 — `./x` is accepted as a file name where `RejectFileName` would refuse it

**Status:** cosmetic, unfixed · **Owner:** post-v1 · **Reversible:** yes

`PUT /__admin/files/./x` answers 201 and stores the file as `x`. The rule in
`stub.RejectFileName` refuses a name that is not already in cleaned form, and it
is applied — but Go's `ServeMux` normalises the request path before the handler
sees it, so the name that reaches the check is already `x`.

Nothing escapes: `..` climbing is refused by the mux with a 404 well before the
handler, and no driver joins a name onto a filesystem path. The cost is only
that the caller's name and the stored name differ, which is the exact thing the
rule's own comment says it exists to prevent — so the rule is honest and the
router quietly makes it moot for this one shape.

**To change:** compare against `r.URL.EscapedPath()` rather than the routed
value, or register the files routes on a mux that does not redirect. Both are
more machinery than the problem currently justifies.
