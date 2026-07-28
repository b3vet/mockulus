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

## Templating

### T1 [S1] — `{{ }}` HTML-escapes its output; WireMock does not escape at all

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

`{{size request.query.tier}}` over `?tier=go-ld` gives WireMock `1` (the number
of values) and mockulus `5` (the length of the string). Same root cause as T4.

### T6 [S1] — `jsonPath` selecting a non-scalar node renders Go's `fmt` output

```
request body {"xs":[10,20,30],"who":{"name":"ada","city":"london"}}
WireMock     arr=[[10,20,30]]      obj=[{ "name" : "ada", "city" : "london" }]
mockulus     arr=[[10 20 30]]      obj=[map[city:london name:ada]]
```

A stub echoing a subtree emits Go internals. Scalars, indices, negative indices
and wildcard-with-`#each` all agree.

### T7 [S2] — `now` offsets in months and years use fixed 30- and 365-day arithmetic

On 2026-07-28, `offset='1 months'` gives WireMock `2026-08-28` and mockulus
`2026-08-27`; `offset='2 years'` gives `2028-07-28` against `2028-07-27`. Cause:
`internal/template/helpers.go` `parseOffset` maps month to 30×24h and year to
365×24h where WireMock does calendar arithmetic. Days and hours agree.

### T8 [S2] — `now format='XXX'` at a zero offset

`XXX` renders `Z` on WireMock and `+00:00` on mockulus, because Java's pattern
special-cases UTC and the Go layout `-07:00` cannot. Non-zero offsets agree.

### T9 [S2] — `math` division returns a float where WireMock rounds to an integer

```
{{math 1 '/' 3}}   WireMock 0      mockulus 0.3333333333333333
{{math 10 '/' 4}}  WireMock 3      mockulus 2.5
{{math '7' '/' '5'}} WireMock 1    mockulus 1.4
```

WireMock rounds half-up rather than truncating. `{{math 5 '%' 2.5}}` renders
`0.0` there and `0` here.

### T10 [S2] — `base64Body` is templated by mockulus and not by WireMock

A `base64Body` decoding to `tier={{request.query.tier}}` renders `tier=gold`
here and stays literal there. mockulus is following its own SPEC §10.1 ("after
file/base64 resolution"), so this is spec-versus-oracle rather than a slip — but
§5.5 does not list it, and the compatibility claim currently covers it.

### T11 [S3] — `lookup` with an absent key

WireMock renders the whole map's `toString` (`lk={tier=gold}`); mockulus renders
nothing. mockulus is arguably the better answer.

### T12 [S3] — `{{substring request.body 0 4}}` on an empty body

WireMock renders `0`, mockulus renders the empty string.

### T13 [S2] — JSONPath dialect gaps visible through the helper

`$.items[?(@.q>1)].sku`, `$.items[?(@.sku=="A1")].q` and `.length()` all render
empty here and resolve there. Deep scan `$..sku` agrees on values, though
WireMock prepends a spurious empty element. Overlaps J4 and J5 below.

### T14 [S3] — `request.id`

mockulus returns the `X-Request-Id` header when present; WireMock always a fresh
UUID. An extension, and undiffable either way.

---

## Response definition

### R1 [S1] — a stub-declared `Content-Length` that disagrees with the body

```
stub     response {"status":200,"body":"abcdefgh","headers":{"Content-Length":"3"}}
WireMock Content-Length: 3, then all eight bytes
mockulus Content-Length: 3, then zero bytes, then close
```

Both are wrong and mockulus's is worse: a client blocks or reports an incomplete
read. WireMock at least delivers the body.

### R2 [S2] — `Content-Type` declared as an array

`["application/json","text/plain"]` becomes `text/plain;charset=utf-8` on
WireMock (one header, neither value verbatim) and
`application/json, text/plain` here.

### R3 [S2] — non-ASCII header values go out in different encodings

`"X-Caf":"café"` is sent as ISO-8859-1 (`caf` + `0xE9`) by WireMock and as UTF-8
(`caf` + `0xC3 0xA9`) here. Same root cause as M2 below, on the response side.

### R4 [S2] — a negative `fixedDelayMilliseconds`

`-5` is accepted by WireMock and refused here 422 code 10
(`fixedDelayMilliseconds must not be negative`). Fail-loud, and defensible, but
undocumented.

### R5 [S2] — a non-integer `fixedDelayMilliseconds`

`10.5` is accepted there and refused here. Same shape as R4.

### R6 [S2] — a non-string `body`

`"body":7` is coerced to `7` by WireMock and refused here 422.

### R7 [S2] — a `status` given as a string

`"status":"200"` is coerced there and refused here 422.

### R8 [S2] — unpadded `base64Body`

`SGVsbG8` (no padding) decodes to `Hello` there and is refused here 422, because
`base64.StdEncoding` requires padding. The padded and url-safe forms agree.

### R9 [S2] — an empty header value array, in the opposite direction

`"headers":{"X-A":[]}` is refused by WireMock 422 (`No value for X-A`) and
accepted here 201, serving no such header.

### R10 [S3] — a one-element header array is collapsed by WireMock's echo

`{"X-One":["solo"]}` is echoed as `{"X-One":"solo"}` there and kept as an array
here. Serving agrees.

### R11 [S3] — two case-variant spellings of one header name are folded by the echo

`{"X-DUP":"first","x-dup":"second"}` is echoed as `{"X-DUP":["first","second"]}`
there and verbatim here. Both emit two header lines in the same order, so
serving agrees.

### R12 [S3] — JSON number rendering in `jsonBody`

WireMock rewrites exponent notation in plain decimal (`1e2` becomes `100`,
`1.5e-3` becomes `0.0015`); mockulus emits the submitted text verbatim. Both
preserve `1.0`, a 20-digit integer and an over-precise decimal byte for byte.
Structurally equal, so §5.6's comparison passes it.

### R13 [S3] — non-ASCII inside `jsonBody`

WireMock emits raw UTF-8 (`café ☕`); mockulus emits the `\u00e9` and `\u2615`
escapes instead, and spells a control character `\u001f` where WireMock spells it `\u001F`. Structurally equal, so the diff passes it.

### R14 [S2] — `statusMessage: ""`

WireMock sends an empty reason phrase as asked (`HTTP/1.1 200 `); mockulus
treats empty as unset and sends `OK`. This is D-OPEN-9 in `DECISIONS.md`, now
measured: **the probe that entry asked for has been done, and WireMock honours
the empty string.**

### R15 [S2] — the reason phrase for a status with no IANA name

`599` gives `HTTP/1.1 599 599` there and `HTTP/1.1 599 status code 599` here;
`451` gives `Unavailable for Legal Reason` against `Unavailable For Legal
Reasons`. This is the open half of D-OPEN-5. Neither R14 nor R15 is visible to
the differential replay, which compares status code, headers and body.

---

## Matchers (body and header)

### M1 [S2] — a null matcher operand is accepted and silently coerced to `""`

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

With `Content-Type: text/plain; charset=ISO-8859-1` and an operand of `café`,
the two servers are again mirror images: WireMock decodes the body using the
declared charset before applying the matcher, mockulus compares the raw bytes as
text. Under `charset=UTF-8` both agree, so the divergence is exactly the charset
parameter being read by one side and not the other.

### M4 [S2] — `caseInsensitive` disagrees on the Turkish dotted and dotless I

An operand of `İstanbul` (U+0130) with `caseInsensitive` matches `istanbul` and
`ISTANBUL` on WireMock and neither here; the mirror holds for `ırmak` (U+0131).
Cause: Java's `equalsIgnoreCase` tries upper- then lower-casing per character,
where Go's `strings.EqualFold` uses simple case folding under which both fold
only to themselves. Every other non-ASCII pair probed agrees and is covered by
the delivered corpus.

### M5 [S2] — `binaryEqualTo` with an empty operand

Matches an empty body on WireMock and not here, because mockulus reports a
zero-length body as absent (`internal/matchers/subject.go`, `Body.Present`) and
returns false before comparing. This is a real hole in the "only `{absent:true}`
matches an empty body" rule that `matchers-core-empty-body-001` pins.

### M6 [S2] — unpadded base64 in `binaryEqualTo`

`aGVsbG8` is accepted there and refused here 422, same cause as R8. Base64 with
embedded whitespace is refused by both.

---

## JSON body matching

### J1 [S1] — `equalToJson` compares numbers at float64 precision

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

Body `{"a":1} tail` matches on WireMock and not here, consistent with Jackson
stopping at the first complete value while Go rejects trailing bytes. Trailing
*whitespace* agrees.

### J3 [S2] — single-quoted member names in the request body

Body `{'a':1}` matches on WireMock and not here. Scoped: the unquoted form
`{a:1}` is refused by both, so the leniency is single quotes specifically
(Jackson's `ALLOW_SINGLE_QUOTES` on, `ALLOW_UNQUOTED_FIELD_NAMES` off).

---

## JSONPath

### J4 [S1] — `length()` compiles to a child key and silently never matches

`$.xs.length()` is accepted 201 by both servers; over body `{"xs":[1,2]}`
WireMock matches and mockulus does not, because `readName` reads `length()` as a
bare member name. This is the failure mode SPEC §6.7 explicitly forbids —
"unsupported syntax → 422 at registration, never a silent non-match" — so it is
both a missing feature and a fail-quiet.

### J5 [S2] — index unions are refused at registration

`$.items[0,2]` is accepted there and 422 here; `parseBracket` in
`internal/jsonpath/jsonpath.go` has no union branch. SPEC §6.7 lists unions in
the committed dialect.

### J6 [S1] — decimals lose their fraction on the way to the inner matcher

`{"matchesJsonPath":{"expression":"$.total","equalTo":"5.0"}}` over body
`{"total":5.0}` matches there and not here, because mockulus decodes to float64
and re-renders as `5`. SPEC §6.7 states the opposite in so many words: "a
selected non-string is rendered to text first, exactly (5 becomes "5", 5.0
becomes "5.0")". Integers agree.

### J7 [S2] — a definite path selecting an array node quantifies differently

`{"expression":"$.tags","equalTo":"red"}` over `{"tags":["red"]}` matches there
and not here. WireMock cannot distinguish "definite path selecting an array"
from "list of hits" and applies the inner matcher element-wise; mockulus carries
definiteness through and renders the node as `["red"]`. The bare form agrees —
the conflation only shows in the nested form.

### J8 [S2] — a filter existence term is presence there and truthiness here

`$.items[?(@.flag)]` matches `{"flag":null}` and `{"flag":{}}` on WireMock and
neither here, because mockulus routes the operator-less term through the same
emptiness rule the bare form uses.

### J9 [S2] — WireMock's parser accepts input `encoding/json` rejects

A bare `plain text` body under `$` parses there as a JSON string and matches;
`{"a":1} junk` under `$.a` matches there. Both 404 here. Same permissive-parser
cause as J2.

### J10 — unverifiable: WireMock 500s on a null hit reaching an inner matcher

`{"expression":"$..v","equalTo":"null"}` over `{"a":{"v":null}}` returns HTTP 500
with a Jetty `NullPointerException` page. Reproduced with an inner `{"matches":".*"}`
too, so it is not specific to `equalTo`. mockulus answers 404, which is what
§6.7 specifies; the rule is simply not verifiable against this oracle.

---

## URL matching

### U1 [S2] — a bare `?` is dropped by WireMock and kept by mockulus

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

mockulus refuses 422 ("only one URL criterion may be given"); WireMock's
`UrlPattern.fromOneOf` silently keeps the first and discards the rest.

---

## Query parameters

### Q1 [S2] — an empty `equalTo` operand loses to a non-empty sibling value

`{"x":{"equalTo":""}}` against `?x=&x=v` is 404 on WireMock and 200 here. Also
diverges for `?x=v&x=`, `?x&x=v`, `?x=&x=a`, `?x=a&x=`, `?x=&x=&x=v`; agrees for
every single-valued spelling. WireMock's journal shows it parsed the values as
`["","v"]`, so this is the multi-value match rather than parsing — and its own
any-of works normally for every other matcher over the same request. mockulus's
answer is the one SPEC §5.2's any-of rule states; WireMock's looks like a quirk,
but it is a real difference on the wire. The header analogue reproduces it.

### Q2 [S2] — a semicolon in the query string

`?a=1;b=2` against `{"a":{"equalTo":"1;b=2"}}` is 200 on WireMock and 404 here.
WireMock splits on `&` only, so the semicolon is an ordinary character in the
value; Go's `url.ParseQuery` has rejected semicolons since 1.17 and **drops the
whole element**, so the parameter disappears rather than carrying the semicolon.
Any client sending a semicolon in a filter or range expression loses that
parameter entirely here. The escaped spelling agrees.

---

## Admin API

### A1 [S2] — a non-UUID mapping id in the path

`GET|PUT|DELETE /__admin/mappings/not-a-uuid` gives WireMock 400 code 10
(`not-a-uuid is not a valid UUID`) and mockulus a bare 404 with no body. SPEC
§5.1 pins "404 with an empty body if the id is unknown" for a well-formed but
unregistered UUID — which agrees — but a segment that is not a UUID at all is a
different case, and the two answer it with different status classes.

### A2 [S2] — an upper-case mapping id in the path does not resolve here

A stub registered as `a1000008-00ff-…` is found by
`GET /__admin/mappings/A1000008-00FF-…` on WireMock and 404s here, for GET, PUT
and DELETE alike. SPEC §5.2 says the id is parsed case-insensitively and
canonicalised to lower case; mockulus does that on the write path and compares
the path segment as text.

### A3 [S2] — precedence inside one import payload is reversed

Two equal-priority stubs competing for one path, in one `import` payload: the
**first** element wins on WireMock and the **last** wins here, because WireMock
applies the array back to front. Same cause with a duplicate id repeated inside
one payload under OVERWRITE — WireMock keeps the first occurrence's document,
mockulus the last. Across the payload/store boundary the two agree, so the
disagreement is confined to stubs that compete with each other *and* arrive in
the same batch — which is what a fixture file of overlapping stubs looks like.

### A4 [S2] — `importOptions` present without `duplicatePolicy`

Resolves as IGNORE on WireMock (a null policy is not OVERWRITE) and as the
documented default OVERWRITE here. When the whole `importOptions` object is
omitted both agree on OVERWRITE. Distinct from deviation #21, which records the
mirror case.

### A5 [S3] — a stub registered with no `response` key

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
