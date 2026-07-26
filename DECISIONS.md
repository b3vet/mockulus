# Decisions

Questions that were genuinely open — where the implementation has taken a
position that is defensible but reversible, and someone should confirm it before
v1.0. Each entry records what was chosen, why, what it would cost to change, and
what evidence would settle it.

Settled decisions live in the ADR (SPEC §3) and the deviation list (SPEC §5.5).
This file is the waiting room, not the record — an entry marked **CLOSED** keeps
its reasoning here and its consequence there.

---

## D-OPEN-1 — Multi-value selection under `caseInsensitive` — CLOSED

**Resolved 2026-07-26: stands as a deviation.** Plain any-of is what mockulus
does — a repeated header or query parameter satisfies an `equalTo` +
`caseInsensitive` criterion when *any* value matches. WireMock appears to pick
the minimum-distance value instead.

Closing it this way rather than chasing parity: the divergent corner needs all
three of a multi-valued key, `caseInsensitive`, and a near-miss sibling at
non-zero distance, and reproducing it would put a distance computation on the
matching hot path against SPEC §16.3 rule 1. Recorded in the §5.5 deviation
list; no code change.

---

## D-OPEN-2 — `hasExactly` semantics — CLOSED (not a decision)

**Resolved 2026-07-26: nothing to decide here.** `hasExactly` is rejected 422 in
v1 and is a roadmap item; this entry only recorded a probe result so whoever
implements it does not build the intuitive-but-wrong version. The finding —
that it is size-equality plus each-pattern-matches-some-value, so
`hasExactly[{equalTo:"a"},{equalTo:"a"}]` matches `?x=a&x=zzz` — now lives with
the roadmap item where it belongs.

---

## D-OPEN-3 — Near-miss distance weighting — CLOSED

**Resolved 2026-07-26: no ordering is claimed.** Near misses are a list of
stubs *similar* to a request that did not match — a debugging aid, nothing more.
No matching decision depends on the order, WireMock's ranking is not reproduced,
and the `[DH]` in SPEC §6.8 is closed on those terms rather than by
reverse-engineering a formula. Recorded as deviation #28.

The scoring that exists (a criterion wholly absent costs a full unit, a close one
costs proportionally less by normalized edit distance) stays as an implementation
detail, free to change, because nothing is promised about it.

---

## D-OPEN-4 — Response framing — CLOSED (not a decision)

**Resolved 2026-07-26: this is the tool's default, not an open question.**
mockulus frames a response the way `net/http` does. The entry existed because
framing is observable and WireMock's Jetty makes different choices in corners
(when it chunks, when it sets Content-Length), but nothing depends on matching
those, no case asserts them, and no client has been shown to care.

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

## D-OPEN-6 — Sibling matcher keys on one document — CLOSED

**Resolved 2026-07-26: conjunction stands, and is now documented as a
deviation.** `{"contains": "a", "doesNotContain": "b"}` requires both. WireMock
honours only the first key its binding visits and discards the rest, so the same
document means *less* there — the stub matches requests its author wrote a
criterion to exclude, silently.

Conjunction is what a person writing two criteria intends, and it errs toward
refusing more rather than less. Done: the comment on `Compile` in
`internal/matchers/compile.go` claimed WireMock agreed and no longer does;
recorded as deviation #26; pinned by `matchers-sibling-keys-001`.

---

## D-OPEN-7 — `and` / `or` with a single operand — CLOSED

**Resolved 2026-07-26: matched to WireMock.** Both now require at least two
operands and answer 422 for a one-operand form, as WireMock does. A combinator
over a single matcher is that matcher, so accepting it cost nothing at match
time — but a mappings file that registers here and is refused there cannot move
back, which is the direction of D2 that matters.

Done in `compileCombinator`; recorded as deviation #27 (a note that the arity is
part of the accepted surface, not that the two differ); pinned by
`matchers-sibling-keys-001`.

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

## D-OPEN-11 — `deleteWhere` behind the journal — CLOSED (was unfinished work)

**Resolved 2026-07-26: finished rather than decided.** This was never a
question — it was the tail of the D-OPEN-10 fix. Scenarios were converted when
M4 landed; `ClearJournal` was missed and still issued a `DELETE FROM`, which is
planned as a scan of the persisted view, so an entry written moments earlier was
invisible to it and survived a clear that answered 200.

That is the collection where it mattered most: entries are written continuously
by the request path, and a verification run against a journal that was told to
be empty fails for a reason its author cannot see. It now selects keys through
the watermarked bulk read and removes by key, like mappings and scenarios.
`deleteWhere` has no callers left and is deleted.

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

## D-OPEN-14 — JSONPath body matching allocates on the request path

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

## D-OPEN-15 — `./x` accepted as a file name — CLOSED (not a decision)

**Resolved 2026-07-26: nothing to decide.** `PUT /__admin/files/./x` stores the
file as `x` because Go's `ServeMux` normalises the request path before the
handler sees it, so the name reaching `RejectFileName` is already clean.

Nothing escapes — `..` climbing is refused by the mux well before the handler,
and no driver joins a name onto a filesystem path. The only cost is that the
caller's spelling and the stored name differ for this one shape. Left as is.
