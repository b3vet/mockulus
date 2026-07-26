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

## D-OPEN-10 — Cross-pod read consistency on the rebuild path — CLOSED

**Resolved 2026-07-26: (b) implemented.** `BumpEpoch` publishes the position of
the write behind it, and every pod folds that into its own scan requirement.
Cross-pod reads are now as strong as same-pod ones and SPEC §8's bound holds as
written; §7.3 documents the document and §8 the property.

This one was measured, not reasoned about, and it was the most consequential
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

**What was still open was the other pod's write.** `LoadAll` reads the epoch
*before* the scan and stamps the snapshot with it, so a pod rebuilding because a
peer bumped the epoch could install a view predating that peer's write, stamp it
with the new epoch, and read as converged. The stub was then missing on that pod
until `resync_interval` (5 min) rather than `sync_interval` (1 s). Measured on a
second store handle: 2 misses in about 60 immediate reads, widest window 117 ms.
This is a T3 (multi-pod) property; single-pod deployments were unaffected.

It mattered because §8's propagation bound is the thing the whole architecture
is sold on, and 5 minutes is not 1 second.

**The three options were:** (a) accept it and state the real worst case in §8,
which costs nothing and weakens the headline guarantee; (b) publish the causing
write's position so any pod can fold it into its own scan requirement; (c)
always bulk-load through N1QL at `RequestPlus`, which is cross-pod consistent by
construction and costs the primary index and the scan throughput the range-scan
path exists for (§7.2).

### What (b) turned out to be

`meta::writes` holds one position per vbucket — seqno and partition uuid — in
gocb's own `MutationState` encoding, so it stays legible to anyone holding the
SDK. `BumpEpoch` merges this pod's outstanding positions into it and then bumps
the counter; `LoadAll` reads the epoch, then the document, and hands the union
of the two halves to the scan as `ConsistentWith`. The ordering is the property:
publish before the bump, read the epoch before the document, and every position
the epoch accounts for is one the reload has to observe.

Two things in that sentence do not survive being written the obvious way, and
both fail quietly rather than loudly.

**Merging by `MutationState.Add` keeps the wrong token.** `MarshalJSON` walks
the token slice in order and overwrites each vbucket's `SeqNo`/`VbUUID` as it
goes, so what survives is whichever token came *last*, not whichever is newest —
adding two states together and marshalling therefore keeps the older position
about half the time, and the result looks like a requirement while being weaker
than one. The merge is done at the JSON level instead, under the same
`supersedes` rule the local watermark uses: higher sequence number within a
history, and across histories the incoming position wins, because a failover
restarts a vbucket's numbering and comparing the numbers across UUIDs pins the
requirement to a position that never existed. `MutationState.Internal()` would
have made this easier and is marked unsupported; the JSON is the supported road.

**A shared document plus a plain upsert loses a peer's position** — and the one
it loses is precisely what some third pod is about to need. It is written under
CAS (read, merge, replace at the version read), bounded at eight rounds, and
reported rather than looped. Measured with six handles publishing concurrently:
0 of 6 positions lost.

### Measured

The probe is the one this entry named: a write on one store handle, an immediate
`LoadAll` on a second, a fresh scope per trial. Both arms run against the same
cluster in the same session, the A arm with the published document deleted after
each bump so the reader falls back to the pre-fix guarantee.

| | misses | widest window |
|---|---|---|
| A — local watermark only (pre-fix), idle node | 1 / 480 | 176 ms |
| A — local watermark only (pre-fix), eight-way write pressure | 98 / 120 | 6.4 s |
| B — published watermark, same pressure | 0 / 120 | — |

The two A rows are the reason this sat open long enough to need an entry. On an
idle node the disk queue drains between the write and the read, so the pre-fix
arm is wrong roughly once in five hundred and looks like a flake; under the
write pressure a CI suite actually generates it is wrong four times in five, for
seconds at a time. The window is not the interesting number either way — a pod
stamps that view with the epoch it read and is then read as converged, so what
the deployment sees is not a 6 s stall but a stub missing until the next
`resync_interval`.

Re-measured on a second run against a fresh single node, the same probe gives
102/120 and 90/120 on the pressure arm and 0/120 on the idle one — which is the
shape rather than a contradiction: idle is the arm that has to be run into the
hundreds before it fails once.

That re-measurement is also what found the hole below, because the B arm did
*not* reproduce as 0: it came back 1/120, and the one was the fix's own fallback
path. With that closed, B is 0 / 480 across four runs, with one reload failing
loudly — which is the trade being made and not a leftover.

The document measured 30,809 bytes across all 1024 vbuckets — 30.1 bytes an
entry, and that is the full spread, not a sample of it. (29,184 bytes on the
re-measurement, which is the same document with shorter sequence numbers in it:
what is bounded is the entry count, and the digits ride on how long the
deployment has been writing.) It is not pruned. An
entry could in principle be dropped once every pod has observed it, but "every
pod" is not knowable from inside one of them, and the two ways to approximate it
are worse than the 30 KB: a wall-clock expiry makes the guarantee depend on
cross-pod clock agreement, where a fast clock drops a live requirement and says
nothing, and pruning on a refused scan cannot attribute the refusal to a vbucket
and so throws away every peer's position to remove one.

### What it costs

One KV get plus at most one CAS write per admin write, and one KV get per bulk
read — nothing on the request path, and nothing at all on a replica that only
serves and never reloads. A pod with no outstanding writes publishes nothing, so
a deployment that never takes an admin write never creates the document; a pod
on the N1QL fallback never reads it, because `RequestPlus` is already cross-pod
consistent.

### What it does when it breaks

Every failure that leaves the *requirement* still meetable degrades to the
local-only guarantee and logs rather than refusing to rebuild, because a
stale-by-one-resync snapshot still serves and no snapshot does not (§4.6). The
one that does not — a scan that times out meeting the requirement — fails the
reload on purpose, for the reason set out below. An absent document is the
ordinary state of a fresh keyspace
and is not a warning. An unreadable one is — and it is the one failure a *writer*
repairs rather than routes around: a document nobody can decode carries no
requirement anybody can use, and left alone it would stop every pod in the
deployment publishing for as long as it sat there, so the next publish replaces
it at the version it read. A *refused* one is the interesting
case: a failover gives the vbucket a new history and the published position
still names the old one, so the server answers vb-uuid mismatch to that scan and
will answer it forever. The scan is retried without the published half, and only
if that retry succeeds is the document distrusted — at the exact CAS it failed
at, so a republish earns its trust back and the merge heals it for every vbucket
someone writes to. Measured against a hand-poisoned document — a position naming
a vbucket history that never existed — the reload keeps every one of its five
files and takes 96 ms doing it, the reload after it 94 ms, and a republish is
enough for the next one to try the document again. The clock does not separate
those first two, because the server refuses a vb-uuid mismatch immediately
rather than waiting the scan out; what the memo saves is the round trip, and
that is pinned by unit test rather than by the stopwatch.

**That fallback was itself a way to be silently stale, and it took a second
measurement to see it.** It was first written to run after *any* failed scan:
retry without the published half, and if that answers, take it. But a scan
carrying a requirement does not fail only when the requirement is impossible —
it **waits** for the vbucket to reach the position, so the ordinary failure
under load is the wait outrunning the scan budget and returning a timeout.
Retrying that without the requirement asks the same question with the guarantee
taken out, and it answers: a whole collection, no error, missing the write the
reload was triggered by, stamped with the new epoch and read as converged. This
entry's own bug, reached through its fix, about once in 120 reloads under
eight-way pressure.

So the fallback is now allowed only where it is a verdict rather than a delay,
and the server is what says which: vb-uuid mismatch is status 168 and reaches
gocb as `ErrMutationTokenOutdated`, confirmed against the poisoned document
above. A timeout fails the reload instead — the previous snapshot stands, the
pod is visibly behind rather than wrongly converged, and the poller's next tick
retries it (§4.6). Across 480 trials that costs one loud reload failure and buys
every silent one.

**The regression test** is `sync-cross-pod-store-read-004`: four rounds of write
on one replica, `GET /__admin/files` on another, every replica taking both
parts. The assertion is a plain `expect` and that is the whole case — an
`expect_eventually` would be satisfied by the next reload whoever causes it,
since cases share a deployment and every concurrent admin write bumps the same
epoch, so a pod holding a stale view gets repaired by a neighbour's traffic
before any window closes. The file listing is a bulk read of the store rather
than a render of the pod's snapshot, so it admits no "not yet".

**Left open, deliberately:** scenario state writes note their positions but do
not bump the epoch, so they reach the document only when the next admin write
publishes. `GET /__admin/scenarios` on a peer can therefore still read past a
state another pod has just written. It is a narrower window than this entry's —
scenario state is read on the request path by a KV get, which is always current
— but it is the same shape, and it is not closed here.

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

## D-OPEN-14 — JSONPath body matching allocates on the request path — CLOSED

**Resolved 2026-07-26: fixed — a definite path is now evaluated over the body
as it arrived.** The entry recorded the one measured violation of SPEC §16.3
rule 1: every other matcher shape was allocation-free per request, and a
`matchesJsonPath` body criterion decoded the body into a whole `map[string]any`
in order to read one leaf. That decode was the 1,152 bytes.

`BenchmarkMatch/mixed/1000`, dev-arm64, median. The two builds were run
alternately rather than one after the other — this machine drifts several
percent as it warms, enough to have invented most of a result on its own.

| Shape | ns/op | B/op | allocs/op |
|---|---|---|---|
| `exact`, before | 1,006 | 0 | 0 |
| `exact`, after | 1,076 | 0 | 0 |
| `regex`, before | 1,064 | 0 | 0 |
| `regex`, after | 1,057 | 0 | 0 |
| `jsonpath`, before | 1,508 | 1,152 | 29 |
| `jsonpath`, after | **861** | **100** | **4** |

The `exact` row is the one to be honest about: it moved 7% the wrong way, and
nothing was done to it. That request is a GET with no body, so it never reaches
the scanner, and its CPU profile is unchanged function for function either side
— what moved is where the linker put the hot loop once `internal/matchers` grew.
Measuring the two halves of that change separately gives ~2% each, which is the
give-away, because layout effects do not add up and real work does. It is inside
§16.2's 15% band; BASELINE.md records it against the row so it is not mistaken
later for a regression with a cause.

**What landed.** `internal/jsonpath/scan.go` walks the raw body once, carrying
the path's steps with it, and comes back with the byte range the path selects —
no tree, and for the bare form no decode at all. It applies only to a DEFINITE
path, which is what practically every stub writes (`$.customer.id`,
`$.card.brand`); an indefinite one keeps the tree evaluation, where it was
already correct. `internal/matchers` reaches it through two optional
capabilities, `rawJSON` on the subject and `JSONPathScanner` on the evaluator,
so a subject that has no undecoded document and an engine that will not take a
path both fall back rather than having to be special-cased.

Two things the scan does that a faster version would not, and both are load
bearing. It validates the WHOLE document rather than stopping at the node, so
`{"card":{"brand":"visa"}} junk` stays the non-match `json.Unmarshal` makes it —
and it refuses the numbers the decoder refuses, because one decode error rejects
a document entirely. And it hands the selected node to `encoding/json` over that
node's own bytes rather than passing text along, because the nested form
compares what was selected AS TEXT: a number reformatted or an object's members
reordered would change the answer `equalTo` gives.

**Why the equivalence is believable.** It is tested, not asserted.
`TestScanMatchesTree` runs 38 expressions over 102 documents and requires the
same `Result` — the same node, of the same Go type — from both evaluators, plus
the same verdict on whether the document is JSON at all;
`TestMatchesJSONPathScansAndDecodesAlike` does the same one level up, at the
seam where the answer actually reaches a client, over both forms and both
polarities. `FuzzScanEquivalence` fuzzes expression and document together and
found nothing in 75M executions.

That a suite runs is not evidence that it would notice. Eleven deliberate
breakages were put into the scan one at a time, and all eleven were caught:
`null` counted as a present value; the FIRST duplicate key winning instead of
the last; the tail left unvalidated; the number range check dropped; the nesting
limit lifted; a negative index let into the scanner; the empty-collection test
reading length instead of looking past whitespace, so `[ ]` counts as non-empty;
`plainBody` admitting escapes and non-ASCII, so strings get unescaped by hand;
`keyIs` comparing a key's raw bytes without decoding it; `decodeNode` handing a
string's bytes on undecoded; and a scalar in the path's way selecting itself
rather than nothing. `TestScanMatchesTree` killed ten of the eleven and
`TestMatchesJSONPathScansAndDecodesAlike` eight, three fell to
`FuzzScanEquivalence` on its seed corpus alone, and lifting the nesting limit
took the test binary down with a stack overflow — which is the cap earning its
second job.

**What remains, and why.** The bare form allocates nothing — 0 B, 0 allocs,
which `TestBareFormScanAllocatesNothing` holds it to. The nested form costs 4,
and stepping the seam one call at a time says where they go: materializing the
selected node as an `any` is 2, rendering it to text is free, and handing the
`KeyValues` subject to the inner matcher is the other 2 — the subject escapes
because `Matcher.Match` takes an interface.

Both pairs are the shape of the seam rather than the evaluation. Removing the
first means the engine handing over text instead of a value, which is exactly
the equivalence above given up; removing the second means a pooled subject on a
matcher shared by every request. 100 bytes and a scan is not what §16.3 rule 1
was written about.

**Also unfixed, deliberately:** a path with a NEGATIVE index (`$.items[-1].sku`)
is definite but not scanned. Counting from the end needs the array's length,
which one forward pass does not have; getting it means scanning the array twice,
and a path carrying several of them would multiply what a body costs. Those keep
the decode. `Path.Scannable()` is where that line is drawn and
`TestScannableIsDefiniteWithoutNegativeIndices` is what stops it moving by
accident.

---

## D-OPEN-15 — `./x` accepted as a file name — CLOSED (not a decision)

**Resolved 2026-07-26: nothing to decide.** `PUT /__admin/files/./x` stores the
file as `x` because Go's `ServeMux` normalises the request path before the
handler sees it, so the name reaching `RejectFileName` is already clean.

Nothing escapes — `..` climbing is refused by the mux well before the handler,
and no driver joins a name onto a filesystem path. The only cost is that the
caller's spelling and the stored name differ for this one shape. Left as is.

---

## D-OPEN-16 — A local write requirement outlives the vbucket it names

**Status:** open · **Owner:** post-v1 · **Reversible:** yes

Pre-existing, and surfaced while verifying D-OPEN-10 rather than introduced by
it. A pod's own mutation token is cleared only by a *successful* scan that
observed it (`writes.observed`). If the vbucket it names fails over before the
next reload, the requirement can never be satisfied on the surviving branch —
and the pod will not stop asking for it until it happens to write to that
vbucket again and replace the token.

Every reload fails in the meantime. That is the loud direction, not the silent
one: the previous snapshot keeps serving, the failures are counted and logged,
and the poller retries (SPEC §4.6). But it is unbounded in time, and it clears
itself by luck rather than by design.

The published half already handles this — the fallback introduced with
D-OPEN-10 recognises the server's `ErrMutationTokenOutdated` verdict and drops
the impossible requirement. The local half has no equivalent because it never
needed one before.

**To change:** apply the same recognition to a scan that fails on a *local*
requirement — a vb-uuid mismatch means that history is gone, so the token
naming it is worthless and should be dropped rather than retried.

**To settle:** it needs a failover to reproduce, which the single-node test lane
cannot stage. A multi-node topology, or an injected `ErrMutationTokenOutdated`
at the driver seam, would pin it.
