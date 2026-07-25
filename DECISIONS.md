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
