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

## D-OPEN-5 — `statusMessage` reason phrases

**Status:** not implemented · **Owner:** M6 · **Reversible:** at a cost

WireMock emits `statusMessage` as the HTTP/1.1 reason phrase, with its own
quirks: CR and LF each become `?`, the phrase is ISO-8859-1 encoded, and the
fallback when absent is Jetty's phrase table rather than IANA's (`500` →
`Server Error`, `420` → `Enhance your Calm`).

Go's `net/http` does not expose the reason phrase — `WriteHeader` always emits
the canonical text. Emitting a custom one requires hijacking the connection and
writing the status line by hand, which would move every stub that sets
`statusMessage` onto the fault-injection path.

SPEC §5.2 currently marks `statusMessage` as supported on HTTP/1.1. Either the
implementation grows a hijacking path for it, or the spec row and the deviation
list need to say it is accepted and stored but not conveyed. **This one is a
real inconsistency between the spec and the code today**, not just a deferral.
