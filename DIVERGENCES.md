# Differential findings

Behaviours where mockulus and pinned WireMock 3.13.2 answer the same stub and
the same request differently, found while growing the `wm: verified` corpus to
the §20 M6 bar. Each one was written as a corpus case first, failed the
differential run, and was then reproduced by hand against a standalone
`wiremock/wiremock:3.13.2` container and a locally built mockulus. None of them
is in the corpus: a case that records a divergence as expected behaviour would
turn the compatibility oracle into a rubber stamp.

None is covered by the deviation list in SPEC §5.5, which is what makes this
file a backlog rather than a record. Every entry ends up in one of three places
and none of them is here:

- **Fix it** — the answer is wrong on its own terms, or wrong under a rule this
  spec already states. Lands with the `wm: verified` case that was withdrawn.
- **Number it in §5.5** — the difference is deliberate or unavoidable. Lands
  with a `wm: n/a` case pinning what mockulus does, plus a catalog entry in
  `test/e2e/catalog/deviations.yaml`.
- **Record it as unverifiable** — no oracle exists (the two servers listen on
  different ports, a random draw has no expected value, WireMock itself throws).

The severity marks are about what a user notices, not about how hard the fix
is. **[S1]** means a correct stub serves the wrong bytes. **[S2]** means a
mappings file that works on one server does not move to the other. **[S3]**
means the served behaviour agrees and only the stored or echoed document
differs.

---

## Triage

Every entry below carries a **Triage** line: the verdict, whether it blocks v1,
and what the recommended option costs. S is under a day, M a few days, L a week
or more.

**32 fix · 21 accept · 1 unverifiable**, of which **24 block v1**.
**Ten are resolved** — T1, T8, J8, M1, M6, R8, R9 and R14 fixed; Q1 and R15
numbered as deviations #29 and #30. Seven of the ten were in the blocking set,
which leaves **17 blocking items**: 8 under a day, 5 a few days, 3 a week or
more.

The test that decided most of these is not "does WireMock differ" — it is
whether **this spec already commits to an answer**. A dialect gap where §6.7
states the opposite, or a silent coercion where P3 forbids exactly that, is ours
to fix whatever the oracle does. Where the spec says nothing and the difference
is deliberate or inherent to the platform, it is a deviation, and that costs a
numbered §5.5 row, a `deviations.yaml` entry, and a `wm: n/a` case pinning it.

| Item | Verdict | v1 | Cost |
|---|---|---|---|
| A2 | FIX | yes | S |
| A3 | FIX | yes | M |
| J1 | FIX | yes | M |
| J4 | FIX | yes | S |
| J5 | FIX | yes | S/M |
| J6 | FIX | yes | M |
| M1 | FIX | yes | S |
| M3 | FIX | yes | S/M |
| M6 | FIX | yes | S |
| Q2 | FIX | yes | S |
| R1 | FIX | yes | S |
| R11 | FIX | yes | S/M |
| R14 | FIX | yes | S |
| R9 | FIX | yes | S |
| T1 | FIX | yes | S |
| T2 | FIX | yes | — |
| T3 | FIX | yes | S |
| T4 | FIX | yes | S/M |
| T5 | FIX | yes | S |
| T6 | FIX | yes | S |
| T7 | FIX | yes | S/M |
| T8 | FIX | yes | S |
| U1 | FIX | yes | S |
| A1 | FIX | — | S |
| J8 | FIX | — | S |
| M5 | FIX | — | S |
| R2 | FIX | — | S |
| R6 | FIX | — | S |
| R7 | FIX | — | S |
| R8 | FIX | — | S |
| T13 | FIX | — | M |
| T14 | FIX | — | S |
| R15 | ACCEPT | yes | S |
| A4 | ACCEPT | — | S |
| A5 | ACCEPT | — | S |
| J2 | ACCEPT | — | S |
| J3 | ACCEPT | — | S |
| J7 | ACCEPT | — | S |
| J9 | ACCEPT | — | S |
| M2 | ACCEPT | — | S |
| M4 | ACCEPT | — | — |
| Q1 | ACCEPT | — | S |
| R10 | ACCEPT | — | S |
| R12 | ACCEPT | — | S |
| R13 | ACCEPT | — | — |
| R3 | ACCEPT | — | S |
| R4 | ACCEPT | — | S |
| R5 | ACCEPT | — | — |
| T10 | ACCEPT | — | — |
| T11 | ACCEPT | — | S |
| T12 | ACCEPT | — | S |
| T9 | ACCEPT | — | S |
| U2 | ACCEPT | — | S |
| J10 | UNVERIFIABLE | — | S |

### What this triage corrected

The entries below were a first pass and several were wrong. Re-verified against
the current tree and a fresh oracle:

- **Severity was mis-graded on eleven of them.** T7, T9, T10, T11, T12, T14, M4,
  M5, U1, Q1 and Q2 were marked S2 or S3, but each serves different bytes from a
  mapping both servers accept — S1 by this file's own rubric. A1 goes the other
  way: a path segment is not part of a mappings file and both answers are 4xx,
  so it is S3.
- **T13 is mostly wrong and should be struck.** Filter expressions do resolve
  here; `$.items[?(@.q>1)].sku` and `$.items[?(@.sku=="A1")].q` were both
  measured working. What survives of it is J4 — and a union inside a template is
  a serve-time 500, not an empty render.
- **T8's stated cause is false.** Go's `Z07:00` emits `Z` at a zero offset,
  exactly as Java's `XXX` does. It is a two-line fix rather than a platform
  limit, and it has to land with T2, because the default `{now}` format ends
  in that token.
- **J1 is wider than the 2^53 boundary.** Any two decimals that round to one
  `float64` collide, so `{"a":1}` over-matches `{"a":1.0000000000000000000000001}`.
  The same round-trip sits inside J6, so `matchesJsonPath` over-matches too.
- **J2, J3 and J9 are one cause** and want one shared §5.5 number rather than
  three. Each also has a registration-side half these entries omit: the same
  leniency applies to the `equalToJson` operand, which is the D2 direction.
- **J5 understates itself.** `$['a','b']` is not refused — it compiles to a
  child key named `a','b` and never matches, the same fail-quiet class as J4.
- **A5 is three defaults, not one**: absent `response`, `response` without
  `status`, and absent `request` are all filled in by WireMock's echo.
- **M4 has a mirror.** Go folds supplementary-plane case pairs Java never folds,
  so mockulus over-matches there as well as under-matching the Turkish I.
- **A3 carries a trap.** Reversing the apply loop regresses the IGNORE case that
  agrees today, unless the `existing` lookup moves with it.

### A gap this turned up that is not a divergence

`DECISIONS.md` closes **D-OPEN-1** with "Recorded in the §5.5 deviation list".
It is not there: §5.5 has 28 rows, `deviations.yaml` has 28 matching entries,
and none of them is multi-value selection under `caseInsensitive`. Q1 below is
the same behaviour seen from the other side, so the row that closes Q1 is the
row D-OPEN-1 believed it already had.

---

## Templating

### T1 [S1] — `{{ }}` HTML-escapes its output; WireMock does not escape at all

**Triage: FIX** · **v1-blocking** · cost S — WireMock has exactly one escaping mode (NOOP), so a knob adds a second behaviour with no oracle and no user.

**Resolved 2026-07-29.** Fixed — templated output is no longer escaped.

The widest-blast-radius finding here. Any templated body carrying `&`, `<`,
`>`, `"` or `'` is corrupted, which includes most templated JSON.

```
stub     response.body: {"name":"{{jsonPath request.body '$.n'}}"}
         transformers: ["response-template"]
request  POST body {"n":"Ben & Jerry <fine>"}

WireMock {"name":"Ben & Jerry <fine>"}
mockulus {"name":"Ben &amp; Jerry &lt;fine&gt;"}
```

It reaches the request model directly too — `{{request.query.v}}` over
`?v=a%3Cb%26c%22d%27e` renders `a&lt;b&amp;c&#34;d&#39;e`, and `{{request.url}}`
on any multi-parameter query escapes the `&` between parameters.

Cause: `internal/handlebars/eval.go` applies `html.EscapeString` when
`Node.Escaped`. WireMock runs its transformer with escaping off. Turning
escaping *on* to match is not the fix and cannot be: Go emits `&#34;`/`&#39;`
where Java would emit `&quot;`/`&#x27;`. The fix is not escaping. `{{{ }}}` is
raw on both and already agrees.

### T2 [S1] — a helper called with no arguments renders empty

**Triage: FIX** · **v1-blocking** — the registry is already consulted at compile time and P2/§16.3 keep lookups off the render path.

```
stub body  rv=[{{randomValue}}] now=[{{now}}]
WireMock   rv=[kehljm1bcx3af0wfzyzocoi51m1ql5id2m7e] now=[2026-07-28T11:57:36Z]
mockulus   rv=[] now=[]
```

Cause: `internal/handlebars/parse.go` `parseExpression` treats a single token as
a bare path or literal, never a helper call. `{{now}}` is close to the most
commonly written WireMock template expression there is, and it silently renders
nothing — no 422 at registration, no error at serve time.

### T3 [S1] — `../` parent-scope navigation renders empty, and 500s when nested

**Triage: FIX** · **v1-blocking** · cost S — B produces a plausible wrong answer in the one case `../` exists to express.

```
body     parent=[{{#each (jsonPath request.body '$.xs')}}{{../request.method}}{{/each}}]
WireMock parent=[POSTPOSTPOST]
mockulus parent=[]
```

The nested form is worse: `{{#each xs}}{{#each (jsonPath ../request.body '$.ys')}}`
answers 500 `Template render error: jsonPath: no document given` where WireMock
renders normally. Reaching the request from inside an `#each` is the ordinary
way to write a repeated block.

### T4 [S1] — a repeated query parameter or header is not iterable with `#each`

**Triage: FIX** · **v1-blocking** · cost S/M — the values are already in the model and only their shape hides them; B changes three pinned behaviours to fix one.

```
request  ?tag=red&tag=blue, X-Multi: m0 and X-Multi: m1
body     each-q=[{{#each request.query.tag}}{{this}};{{/each}}]
WireMock each-q=[red;blue;]  eachhdr=[m0;m1;]
mockulus each-q=[]           eachhdr=[]
```

Cause: `internal/template/template.go` `headerValues` models a multi-value entry
as a stringer plus an index map rather than a list, so `#each` has nothing to
walk. Indexed access `.[0]` / `.[1]` works and agrees; only iteration is lost.

### T5 [S1] — `size` of a query parameter measures the string, not the values

**Triage: FIX** · **v1-blocking** · cost S — one interface answers `each`, `size` and `join` together.

`{{size request.query.tier}}` over `?tier=go-ld` gives WireMock `1` (the number
of values) and mockulus `5` (the length of the string). Same root cause as T4.

### T6 [S1] — `jsonPath` selecting a non-scalar node renders Go's `fmt` output

**Triage: FIX** · **v1-blocking** · cost S — it removes the Go internals and reaches parity on the array case, which is the one stubs actually hit; the residual is object whitespace and can take a one-line §5.5….

```
request body {"xs":[10,20,30],"who":{"name":"ada","city":"london"}}
WireMock     arr=[[10,20,30]]      obj=[{ "name" : "ada", "city" : "london" }]
mockulus     arr=[[10 20 30]]      obj=[map[city:london name:ada]]
```

A stub echoing a subtree emits Go internals. Scalars, indices, negative indices
and wildcard-with-`#each` all agree.

### T7 [S2] — `now` offsets in months and years use fixed 30- and 365-day arithmetic

**Triage: FIX** · **v1-blocking** · cost S/M — the wrongness is invisible (a valid-looking date) and B makes it permanent.

On 2026-07-28, `offset='1 months'` gives WireMock `2026-08-28` and mockulus
`2026-08-27`; `offset='2 years'` gives `2028-07-28` against `2028-07-27`. Cause:
`internal/template/helpers.go` `parseOffset` maps month to 30×24h and year to
365×24h where WireMock does calendar arithmetic. Days and hours agree.

### T8 [S2] — `now format='XXX'` at a zero offset

**Triage: FIX** · **v1-blocking** · cost S — the stated blocker does not exist and the fix is two lines.

**Resolved 2026-07-29.** Fixed — `XXX` maps to Go's `Z07:00`, and the default format with it.

`XXX` renders `Z` on WireMock and `+00:00` on mockulus, because Java's pattern
special-cases UTC and the Go layout `-07:00` cannot. Non-zero offsets agree.

### T9 [S2] — `math` division returns a float where WireMock rounds to an integer

**Triage: ACCEPT** · cost S — nothing fails silently here — the answer is visibly a number — and B adopts an astonishing rounding rule permanently.

```
{{math 1 '/' 3}}   WireMock 0      mockulus 0.3333333333333333
{{math 10 '/' 4}}  WireMock 3      mockulus 2.5
{{math '7' '/' '5'}} WireMock 1    mockulus 1.4
```

WireMock rounds half-up rather than truncating. `{{math 5 '%' 2.5}}` renders
`0.0` there and `0` here.

### T10 [S2] — `base64Body` is templated by mockulus and not by WireMock

**Triage: ACCEPT** — `base64Body`'s purpose is opaque bytes, WireMock's behaviour is the safer default for the literal-`{{` hazard, and it costs a spec sentence instead of a permanent….

A `base64Body` decoding to `tier={{request.query.tier}}` renders `tier=gold`
here and stays literal there. mockulus is following its own SPEC §10.1 ("after
file/base64 resolution"), so this is spec-versus-oracle rather than a slip — but
§5.5 does not list it, and the compatibility claim currently covers it.

### T11 [S3] — `lookup` with an absent key

**Triage: ACCEPT** · cost S — reproducing another runtime's `toString` is not compatibility, and §10.3's "standard Handlebars" already says which answer we chose.

WireMock renders the whole map's `toString` (`lk={tier=gold}`); mockulus renders
nothing. mockulus is arguably the better answer.

### T12 [S3] — `{{substring request.body 0 4}}` on an empty body

**Triage: ACCEPT** · cost S — WireMock's own `size` disagrees with its own `substring` on the same request, which is a defect rather than a semantic.

WireMock renders `0`, mockulus renders the empty string.

### T13 [S2] — JSONPath dialect gaps visible through the helper

**Triage: FIX** · cost M — the shared engine is the single point of repair; B is a cheap follow-on that closes the P3 hole the helper path still has.

`$.items[?(@.q>1)].sku`, `$.items[?(@.sku=="A1")].q` and `.length()` all render
empty here and resolve there. Deep scan `$..sku` agrees on values, though
WireMock prepends a spurious empty element. Overlaps J4 and J5 below.

### T14 [S3] — `request.id`

**Triage: FIX** · cost S — the project already decided this question for `clientIp` and answered it the other way in the same file.

mockulus returns the `X-Request-Id` header when present; WireMock always a fresh
UUID. An extension, and undiffable either way.

---

## Response definition

### R1 [S1] — a stub-declared `Content-Length` that disagrees with the body

**Triage: FIX** · **v1-blocking** · cost S — it is the only option that both produces a well-framed message and makes the client-observable body identical to WireMock's (3 bytes), which is what lets the….

```
stub     response {"status":200,"body":"abcdefgh","headers":{"Content-Length":"3"}}
WireMock Content-Length: 3, then all eight bytes
mockulus Content-Length: 3, then zero bytes, then close
```

Both are wrong and mockulus's is worse: a client blocks or reports an incomplete
read. WireMock at least delivers the body.

### R2 [S2] — `Content-Type` declared as an array

**Triage: FIX** · cost S — the intent is genuinely ambiguous, refusing is one branch, and it never puts a malformed message on the wire; reproducing Jetty's MimeTypes charset table (needed for….

`["application/json","text/plain"]` becomes `text/plain;charset=utf-8` on
WireMock (one header, neither value verbatim) and
`application/json, text/plain` here.

### R3 [S2] — non-ASCII header values go out in different encodings

**Triage: ACCEPT** · cost S — there is no correct answer on the wire, Go's choice is the one a Go-native server makes, and B would make mockulus *unable* to express a header WireMock also cannot….

`"X-Caf":"café"` is sent as ISO-8859-1 (`caf` + `0xE9`) by WireMock and as UTF-8
(`caf` + `0xC3 0xA9`) here. Same root cause as M2 below, on the response side.

### R4 [S2] — a negative `fixedDelayMilliseconds`

**Triage: ACCEPT** · cost S — a negative duration is a bug in the caller and this is the cheap place to say so, and one shared entry covers R4+R5 for the price of one.

`-5` is accepted by WireMock and refused here 422 code 10
(`fixedDelayMilliseconds must not be negative`). Fail-loud, and defensible, but
undocumented.

### R5 [S2] — a non-integer `fixedDelayMilliseconds`

**Triage: ACCEPT** · cost none beyond R4 — A, and the WM-rewrites-the-document finding is the argument that makes R5 stronger than R4: refusing here is consistent with #22/#24, not merely conservative.

`10.5` is accepted there and refused here. Same shape as R4.

### R6 [S2] — a non-string `body`

**Triage: FIX** · cost S — the coercion is total and information-preserving — after it, the two servers agree byte for byte — so refusing costs D2 and buys nothing P3 was written to protect.

`"body":7` is coerced to `7` by WireMock and refused here 422.

### R7 [S2] — a `status` given as a string

**Triage: FIX** · cost S — this is the single most likely of the family to appear in a real mappings file (any JS/shell generator that stringifies a code produces it), the coercion is….

`"status":"200"` is coerced there and refused here 422.

### R8 [S2] — unpadded `base64Body`

**Triage: FIX** · cost S — the acceptance sets line up exactly after the fallback (unpadded in, wrong-alphabet out), it is a two-line shared helper, and it retires an entry in another section….

**Resolved 2026-07-29.** Fixed — same shared decoder as M6.

`SGVsbG8` (no padding) decodes to `Hello` there and is refused here 422, because
`base64.StdEncoding` requires padding. The padded and url-safe forms agree.

### R9 [S2] — an empty header value array, in the opposite direction

**Triage: FIX** · **v1-blocking** · cost S — it is the one item in this section where mockulus violates P3 in our own tree, WireMock already agrees with the strict answer, and the fix is a single branch that….

**Resolved 2026-07-29.** Fixed — an empty header value array is refused 422.

`"headers":{"X-A":[]}` is refused by WireMock 422 (`No value for X-A`) and
accepted here 201, serving no such header.

### R10 [S3] — a one-element header array is collapsed by WireMock's echo

**Triage: ACCEPT** · cost S — it costs less than the deviation does — it is a display-only change with no behavioural content, and it removes a permanent trap for anyone authoring a `wm: verified`….

`{"X-One":["solo"]}` is echoed as `{"X-One":"solo"}` there and kept as an array
here. Serving agrees.

### R11 [S3] — two case-variant spellings of one header name are folded by the echo

**Triage: FIX** · **v1-blocking** · cost S/M — the value-order reversal is a genuine wrong answer and the token-stream parse is the fix for both halves at once; it is ~20 lines and removes the map from the one….

`{"X-DUP":"first","x-dup":"second"}` is echoed as `{"X-DUP":["first","second"]}`
there and verbatim here. Both emit two header lines in the same order, so
serving agrees.

### R12 [S3] — JSON number rendering in `jsonBody`

**Triage: ACCEPT** · cost S — verbatim is the better answer for a mock (a client asserting on a payload gets the bytes the stub author wrote) and B would break two green `wm: verified` cases to….

WireMock rewrites exponent notation in plain decimal (`1e2` becomes `100`,
`1.5e-3` becomes `0.0015`); mockulus emits the submitted text verbatim. Both
preserve `1.0`, a 20-digit integer and an over-precise decimal byte for byte.
Structurally equal, so §5.6's comparison passes it.

### R13 [S3] — non-ASCII inside `jsonBody`

**Triage: ACCEPT** · cost none beyond R1 — B trades a green verified case for a red one.

WireMock emits raw UTF-8 (`café ☕`); mockulus emits the `\u00e9` and `\u2615`
escapes instead, and spells a control character `\u001f` where WireMock spells it `\u001F`. Structurally equal, so the diff passes it.

### R14 [S2] — `statusMessage: ""`

**Triage: FIX** · **v1-blocking** · cost S — D-OPEN-9's own settle condition was "probe pinned WireMock with `statusMessage: ""` and match it", the probe is done and WM honours it, the change is one field, and….

**Resolved 2026-07-29.** Fixed — an empty `statusMessage` now reaches the wire (closes D-OPEN-9).

WireMock sends an empty reason phrase as asked (`HTTP/1.1 200 `); mockulus
treats empty as unset and sends `OK`. This is D-OPEN-9 in `DECISIONS.md`, now
measured: **the probe that entry asked for has been done, and WireMock honours
the empty string.**

### R15 [S2] — the reason phrase for a status with no IANA name

**Triage: ACCEPT** · **v1-blocking** · cost S — D-OPEN-5 pre-committed to it ("If none does, this becomes a numbered deviation and stops being a question"), no phrase-reading client has been produced in the time….

**Resolved 2026-07-29.** Accepted — deviation #30, pinned by `deviation-default-reason-phrase-001` (closes D-OPEN-5).

`599` gives `HTTP/1.1 599 599` there and `HTTP/1.1 599 status code 599` here;
`451` gives `Unavailable for Legal Reason` against `Unavailable For Legal
Reasons`. This is the open half of D-OPEN-5. Neither R14 nor R15 is visible to
the differential replay, which compares status code, headers and body.

---

## Matchers (body and header)

### M1 [S2] — a null matcher operand is accepted and silently coerced to `""`

**Triage: FIX** · **v1-blocking** · cost S — the defect is one shared decode step, and a single guard is the only shape that cannot be half-applied to a seventh matcher later.

**Resolved 2026-07-29.** Fixed — a null operand is refused 422 for every matcher key.

WireMock refuses every one of these 422 with a pointed message; mockulus
registers them 201 and the stub then matches by a rule nobody wrote.

```
stub     bodyPatterns [{"contains":null}]
WireMock 422  detail "contains operand must be a non-null string"
mockulus 201, echoed back verbatim; POST with body "zzz" then matches
```

The coerced meanings: `equalTo:null` becomes `equalTo ""` (matches nothing,
since an empty body reads as absent), `matches:null` an empty pattern,
`doesNotContain:null` matches only a request with no body, `binaryEqualTo:null`
zero bytes. Cause: `compileOne` in `internal/matchers/compile.go` unmarshals
into a Go string, and JSON null unmarshals into `""` without error.

This is accept-and-behave-differently in exactly the direction principle P3
exists to forbid, and it is the opposite direction from every catalogued
deviation. The same probe found the mirror case: `{"absent":null}` is accepted
by WireMock as `{"absent":true}` and refused here 422 — fail-loud and arguably
right, but not recorded next to deviation #23.

### M2 [S1] — non-ASCII header values match under opposite encodings

**Triage: ACCEPT** · cost S — nothing on the wire declares which reading is meant, both are defensible, and B trades a divergence against a legacy client population for a divergence against the….

The two servers are exact mirror images. With an operand of `café`:

```
field value bytes 63 61 66 C3 A9 (UTF-8)      WireMock 404   mockulus 200
field value bytes 63 61 66 E9    (ISO-8859-1) WireMock 200   mockulus 404
```

Cause: the operand is decoded from the mapping JSON as UTF-8 on both sides, but
Jetty renders header bytes as ISO-8859-1 while Go hands the raw bytes over as a
UTF-8 string. Nothing on the wire declares the encoding, so neither is wrong —
but the same stub plus the same request gives different answers in both
directions. Blast radius: any criterion whose operand carries a character
outside US-ASCII, on any header-valued matcher.

### M3 [S1] — string body matchers do not honour the request's charset

**Triage: FIX** · **v1-blocking** · cost S/M — ISO-8859-1 is the only legacy charset that shows up in practice and it decodes in three lines, which makes the fix cheaper than the deviation.

With `Content-Type: text/plain; charset=ISO-8859-1` and an operand of `café`,
the two servers are again mirror images: WireMock decodes the body using the
declared charset before applying the matcher, mockulus compares the raw bytes as
text. Under `charset=UTF-8` both agree, so the divergence is exactly the charset
parameter being read by one side and not the other.

### M4 [S2] — `caseInsensitive` disagrees on the Turkish dotted and dotless I

**Triage: ACCEPT** — B if the supplementary-plane over-match confirms against the container (one probe), A otherwise — because A alone means numbering a deviation that says "we match….

An operand of `İstanbul` (U+0130) with `caseInsensitive` matches `istanbul` and
`ISTANBUL` on WireMock and neither here; the mirror holds for `ırmak` (U+0131).
Cause: Java's `equalsIgnoreCase` tries upper- then lower-casing per character,
where Go's `strings.EqualFold` uses simple case folding under which both fold
only to themselves. Every other non-ASCII pair probed agrees and is covered by
the delivered corpus.

### M5 [S2] — `binaryEqualTo` with an empty operand

**Triage: FIX** · cost S — accepting costs more than fixing and burns a deviation number on a corner nobody writes on purpose.

Matches an empty body on WireMock and not here, because mockulus reports a
zero-length body as absent (`internal/matchers/subject.go`, `Body.Present`) and
returns false before comparing. This is a real hole in the "only `{absent:true}`
matches an empty body" rule that `matchers-core-empty-body-001` pins.

### M6 [S2] — unpadded base64 in `binaryEqualTo`

**Triage: FIX** · **v1-blocking** · cost S — A, landed as one helper shared with R8 so the two spellings cannot drift apart later.

**Resolved 2026-07-29.** Fixed — unpadded base64 decodes, via the helper shared with R8.

`aGVsbG8` is accepted there and refused here 422, same cause as R8. Base64 with
embedded whitespace is refused by both.

---

## JSON body matching

### J1 [S1] — `equalToJson` compares numbers at float64 precision

**Triage: FIX** · **v1-blocking** · cost M — it is the only option that makes the code say what §5.2 already says, and I checked it keeps all three delivered number cases green….

```
stub     bodyPatterns [{"equalToJson":"{\"id\":9007199254740993}"}]
request  body {"id":9007199254740992}
WireMock 404   mockulus 200
```

9007199254740993 is the first integer not representable in binary64 and rounds
to its neighbour, so a comparison that decodes into a double cannot separate
them; WireMock's json-unit compares as `BigDecimal`. Direction: **mockulus
over-matches** — a stub keyed on a wide numeric id also answers for its
neighbour.

### J2 [S2] — trailing content after a complete JSON document

**Triage: ACCEPT** · cost S — reading a prefix of a body and calling it the body is a worse product than a visible non-match, and the leniency is Jackson's default leaking through rather than….

Body `{"a":1} tail` matches on WireMock and not here, consistent with Jackson
stopping at the first complete value while Go rejects trailing bytes. Trailing
*whitespace* agrees.

### J3 [S2] — single-quoted member names in the request body

**Triage: ACCEPT** · cost S — the accepted set is an accident of Jackson's defaults rather than a WireMock contract, and the corpus already pins two cells where mockulus's strictness *is*….

Body `{'a':1}` matches on WireMock and not here. Scoped: the unquoted form
`{a:1}` is refused by both, so the leniency is single quotes specifically
(Jackson's `ALLOW_SINGLE_QUOTES` on, `ALLOW_UNQUOTED_FIELD_NAMES` off).

---

## JSONPath

### J4 [S1] — `length()` compiles to a child key and silently never matches

**Triage: FIX** · **v1-blocking** · cost S — §6.7 names `length()` in the dialect, so B satisfies P3 while still failing the sentence that lists the feature.

`$.xs.length()` is accepted 201 by both servers; over body `{"xs":[1,2]}`
WireMock matches and mockulus does not, because `readName` reads `length()` as a
bare member name. This is the failure mode SPEC §6.7 explicitly forbids —
"unsupported syntax → 422 at registration, never a silent non-match" — so it is
both a missing feature and a fail-quiet.

### J5 [S2] — index unions are refused at registration

**Triage: FIX** · **v1-blocking** · cost S/M — the spec commits to unions and B pays most of the parser cost while leaving a documented dialect member missing.

`$.items[0,2]` is accepted there and 422 here; `parseBracket` in
`internal/jsonpath/jsonpath.go` has no union branch. SPEC §6.7 lists unions in
the committed dialect.

### J6 [S1] — decimals lose their fraction on the way to the inner matcher

**Triage: FIX** · **v1-blocking** · cost M — §6.7 spells out `5.0` becomes `"5.0"`, so anything less means amending the spec rather than recording a deviation, and B splits the routes.

`{"matchesJsonPath":{"expression":"$.total","equalTo":"5.0"}}` over body
`{"total":5.0}` matches there and not here, because mockulus decodes to float64
and re-renders as `5`. SPEC §6.7 states the opposite in so many words: "a
selected non-string is rendered to text first, exactly (5 becomes "5", 5.0
becomes "5.0")". Integers agree.

### J7 [S2] — a definite path selecting an array node quantifies differently

**Triage: ACCEPT** · cost S — the definite/indefinite model is §6.7's stated design and B trades the coherence of the whole dialect for one spelling — though this is the closest call in the section.

`{"expression":"$.tags","equalTo":"red"}` over `{"tags":["red"]}` matches there
and not here. WireMock cannot distinguish "definite path selecting an array"
from "list of hits" and applies the inner matcher element-wise; mockulus carries
definiteness through and renders the node as `["red"]`. The bare form agrees —
the conflation only shows in the nested form.

### J8 [S2] — a filter existence term is presence there and truthiness here

**Triage: FIX** · cost S — one line makes the implementation match the rule the corpus already states and the oracle already answers.

**Resolved 2026-07-29.** Fixed — an operator-less filter term asks whether the path resolved.

`$.items[?(@.flag)]` matches `{"flag":null}` and `{"flag":{}}` on WireMock and
neither here, because mockulus routes the operator-less term through the same
emptiness rule the bare form uses.

### J9 [S2] — WireMock's parser accepts input `encoding/json` rejects

**Triage: ACCEPT** · cost S — reproducing two Java parsers' leniency means shipping a second JSON grammar for a class of body no correct client sends.

A bare `plain text` body under `$` parses there as a JSON string and matches;
`{"a":1} junk` under `$.a` matches there. Both 404 here. Same permissive-parser
cause as J2.

### J10 — unverifiable: WireMock 500s on a null hit reaching an inner matcher

**Triage: UNVERIFIABLE** · cost S — the code path is live, is about to be edited by J6, and nothing holds it today.

`{"expression":"$..v","equalTo":"null"}` over `{"a":{"v":null}}` returns HTTP 500
with a Jetty `NullPointerException` page. Reproduced with an inner `{"matches":".*"}`
too, so it is not specific to `equalTo`. mockulus answers 404, which is what
§6.7 specifies; the rule is simply not verifiable against this oracle.

---

## URL matching

### U1 [S2] — a bare `?` is dropped by WireMock and kept by mockulus

**Triage: FIX** · **v1-blocking** · cost S — mockulus already treats the marker as "no query" everywhere else it is pinned, so keeping it only in `url`/`urlPattern` is an inconsistency inside mockulus, not a….

```
target             WireMock          mockulus
GET /probe/u?      200 (url:/probe/u) 404
GET /probe/m?      404                200 (url:/probe/m?)
GET /probe/w?      404                200 (urlPattern:/probe/w\?)
```

WireMock appends the query string only when it is non-empty, so the subject for
`/probe/u?` is `/probe/u`; mockulus takes the target verbatim from
`RequestURI` (`internal/match/request.go`, `requestTarget`). Both directions
diverge: a criterion written without a marker is refused here for a request that
carries one, and a criterion written with one is dead there and live here.
`urlPath` agrees, because mockulus cuts at the first `?`.

### U2 [S2] — two URL criteria on one stub

**Triage: ACCEPT** · cost S — refusing a document whose two criteria cannot both be honoured is the established house rule, and the fix costs one paragraph.

mockulus refuses 422 ("only one URL criterion may be given"); WireMock's
`UrlPattern.fromOneOf` silently keeps the first and discards the rest.

---

## Query parameters

### Q1 [S2] — an empty `equalTo` operand loses to a non-empty sibling value

**Triage: ACCEPT** · cost S — the rule mockulus follows is the one this spec states twice, and the oracle's answer here is a NaN-ordering accident rather than a semantic choice.

**Resolved 2026-07-29.** Accepted — deviation #29, pinned by `deviation-multivalue-anyof-001` (closes D-OPEN-1).

`{"x":{"equalTo":""}}` against `?x=&x=v` is 404 on WireMock and 200 here. Also
diverges for `?x=v&x=`, `?x&x=v`, `?x=&x=a`, `?x=a&x=`, `?x=&x=&x=v`; agrees for
every single-valued spelling. WireMock's journal shows it parsed the values as
`["","v"]`, so this is the multi-value match rather than parsing — and its own
any-of works normally for every other matcher over the same request. mockulus's
answer is the one SPEC §5.2's any-of rule states; WireMock's looks like a quirk,
but it is a real difference on the wire. The header analogue reproduces it.

### Q2 [S2] — a semicolon in the query string

**Triage: FIX** · **v1-blocking** · cost S — the WM-correct splitter is already in the tree, and one helper removes the data loss, the false `absent` match and the internal disagreement at once.

`?a=1;b=2` against `{"a":{"equalTo":"1;b=2"}}` is 200 on WireMock and 404 here.
WireMock splits on `&` only, so the semicolon is an ordinary character in the
value; Go's `url.ParseQuery` has rejected semicolons since 1.17 and **drops the
whole element**, so the parameter disappears rather than carrying the semicolon.
Any client sending a semicolon in a filter or range expression loses that
parameter entirely here. The escaped spelling agrees.

---

## Admin API

### A1 [S2] — a non-UUID mapping id in the path

**Triage: FIX** · cost S — A2 has to parse the segment regardless, and the parse-failure branch is then the only thing left to decide — taking 404 there would be choosing a deviation on purpose.

`GET|PUT|DELETE /__admin/mappings/not-a-uuid` gives WireMock 400 code 10
(`not-a-uuid is not a valid UUID`) and mockulus a bare 404 with no body. SPEC
§5.1 pins "404 with an empty body if the id is unknown" for a well-formed but
unregistered UUID — which agrees — but a segment that is not a UUID at all is a
different case, and the two answer it with different status classes.

### A2 [S2] — an upper-case mapping id in the path does not resolve here

**Triage: FIX** · **v1-blocking** · cost S — it makes one rule decide what an id is on both the write and the read path, and it is the same helper A1 needs.

A stub registered as `a1000008-00ff-…` is found by
`GET /__admin/mappings/A1000008-00FF-…` on WireMock and 404s here, for GET, PUT
and DELETE alike. SPEC §5.2 says the id is parsed case-insensitively and
canonicalised to lower case; mockulus does that on the write path and compares
the path segment as text.

### A3 [S2] — precedence inside one import payload is reversed

**Triage: FIX** · **v1-blocking** · cost M — the intra-batch order is unspecified rather than deliberate, and an unspecified difference that silently changes which stub answers is precisely what the….

Two equal-priority stubs competing for one path, in one `import` payload: the
**first** element wins on WireMock and the **last** wins here, because WireMock
applies the array back to front. Same cause with a duplicate id repeated inside
one payload under OVERWRITE — WireMock keeps the first occurrence's document,
mockulus the last. Across the payload/store boundary the two agree, so the
disagreement is confined to stubs that compete with each other *and* arrive in
the same batch — which is what a fixture file of overlapping stubs looks like.

### A4 [S2] — `importOptions` present without `duplicatePolicy`

**Triage: ACCEPT** · cost S — mockulus's answer is the one the spec states and the one a reader of the payload expects, and the behaviour being copied is an uninitialised field.

Resolves as IGNORE on WireMock (a null policy is not OVERWRITE) and as the
documented default OVERWRITE here. When the whole `importOptions` object is
omitted both agree on OVERWRITE. Distinct from deviation #21, which records the
mirror case.

### A5 [S3] — a stub registered with no `response` key

**Triage: ACCEPT** · cost S — §7.2 already commits to verbatim round-trip, and B would have to unwind that plus every document stored under it.

WireMock fills in `"response":{"status":200}` in its echo and read-back;
mockulus omits the member entirely. Serving agrees (both answer 200 with an
empty body), but under §5.6's subset rule WireMock's `response.status` is
required, so any case touching such a document fails the diff.

---

## Gate coverage holes found on the way

Not divergences, but two places where the gate cannot currently assert something
it claims:

- **A repeated request header cannot be sent from a YAML case.** `HTTPStep.Headers`
  is a `map[string]string` applied with `req.Header.Set`, so three YAML keys
  spelling one name produce one field line. `B-REQ-HEADERS`'s own evidence
  sentence says "a repeated header matches when any value satisfies the matcher",
  and no corpus case can produce one — the multi-value rule is proven for query
  parameters only. Fixing it needs a list-valued `headers` form in the step
  schema, or a `gotests/` case.
- **`anyUrl` and method-only stubs are untestable in the shared instance.** A stub
  with no URL criterion answers every request on the deployment, including other
  cases' deliberate 404s, and no priority setting makes it safe. It needs
  `requires: [exclusive]`.
