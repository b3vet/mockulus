# Changelog

All notable changes to this project are documented here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) form.

Versioning follows SPEC §22.5: `v0.<milestone>.x` through M0–M5, carrying **no**
compatibility promise, and `v1.0.0` at M6 exit. After 1.0, the behavior of the
WireMock-compatible surface changes only in majors, and a 422 becoming a
supported feature is a minor.

## [Unreleased]

### Added

- Optional OpenTelemetry tracing (SPEC §14.3), configured by the `tracing.*`
  keys and **off by default**. Turned off it costs one atomic load and a branch
  per request — no span is started and no request context is replaced — so the
  SLOs and the allocation budget of §16 are the same numbers they were. Turned
  on, every mock request becomes one server span carrying the match outcome, the
  serving stub and the snapshot epoch, and every admin request becomes one named
  by endpoint group.
- Traces join the caller's. Sampling is parent-based, so a request arriving with
  W3C trace context follows the decision the caller already made and a mock's
  spans appear inside the trace of the test that drove it; `tracing.sample_ratio`
  governs only the traces a pod starts itself.
- `mockulus_trace_export_failures_total`, because a collector that refuses every
  batch would otherwise be indistinguishable from a quiet one. The reason is
  logged at most once a minute behind it, and export is bounded rather than
  retried for a minute — a collector that has not recovered in fifteen seconds is
  an outage, not a queue.
- The phases underneath a request, as child spans: the match decision and how
  many stubs it evaluated, the scenario read and transition, the template render,
  and the delay. The last one earns its place on its own — a slow response and a
  response told to be slow are the same duration, and only one of them is a
  fault. Each appears only when the phase actually ran, so a plain stub's trace
  stays a server span and a match.
- Snapshot rebuilds and journal flushes as spans that root their own traces
  rather than joining the request that triggered them. Both are shared work: a
  rebuild is coalesced across every write behind it, and a journal batch holds
  entries from many requests, so billing either to one caller would attribute the
  cluster's convergence to whoever arrived first.
- A `traceId` on the journal entry and a `trace_id` on the sampled access-log
  line, for a request that was sampled — the journal is where someone looks after
  the fact, and an entry that cannot name its trace leaves them searching by
  timestamp. Both are absent rather than empty otherwise, so a deployment that is
  not tracing keeps the document and the log line it had.

- The date-time matchers `before`, `after` and `equalToDateTime`, with their
  modifiers `truncateExpected`, `truncateActual`, `applyTruncationLast` and
  `actualFormat`. A 422 becomes a supported feature, which is the only direction
  the compatibility promise allows.

  The rule worth knowing before writing one: **the expected value's type decides
  what is compared.** An expected carrying a zone compares instants — the
  request's offset is honoured and a zoneless request value is read in the pod's
  timezone. An expected with no zone compares wall-clock readings and the
  request's offset is discarded rather than converted, so
  `after: "2021-06-14T12:00:00"` reports `2021-06-14T13:00:00+03:00` as later
  even though that instant is two hours earlier. That is WireMock's behaviour,
  established by probing it rather than by assuming, and an implementation that
  normalises everything to instants reproduces half of it and silently breaks the
  rest.

  `before` and `after` are strict; equality is instant-valued and exact to the
  nanosecond; `actualFormat` replaces ISO parsing rather than extending it; and
  `unix` means epoch seconds while `epoch` means milliseconds.

- **The admin UI has a shell and a stub browser.** Navigation, a token flow, the
  error states worth naming, and a filterable list of the registered stubs with
  a detail view. Read-only in this release; editing follows.

  Every call it makes goes through `@mockulus/admin-sdk`, which the UI takes as
  a workspace dependency. There is no second HTTP layer and no private endpoint
  behind the interface: anything the UI can do is something a `curl` can do,
  and the SDK gets exercised by its first real consumer in the same repository
  that ships it.

  **The admin token is held in `sessionStorage` and nowhere else**, and the
  alternatives were each rejected for a reason. `localStorage` would outlive the
  tab and be readable by every other tab on the origin. A cookie would be
  attached by the browser to requests the UI did not make — including the asset
  loads that are exempt from the token by design — turning a header the SDK
  controls into ambient authority. A URL would put it in history, in the
  `Referer` of anything the page links to, and in every access log on the way.

  Two error states are pages rather than toasts, because both are configuration
  rather than failure: a journal that is off (code `1010`, which is the
  **default** posture, so the message says how to turn it on) and a store that is
  unavailable (code `1020`). A `422` renders the JSON Pointers the server named,
  which is the field that makes it actionable.

- **`api/openapi.yaml`** — an authored OpenAPI 3.1 description of the admin API,
  and the first machine-readable statement of the surface this project has had.
  It types the supported subset strictly, so a field mockulus would refuse at
  registration is a field the contract refuses to describe.

  It is checked against the behavior catalog in both directions by
  `make contract-lint`, which runs in CI: a route the server implements and the
  contract omits is a call a client cannot make, and a route the contract
  invents is one that compiles and then 404s. Neither shows up by reading either
  file alone. The check is against the catalog rather than the spec directly
  because the catalog is already pinned row-by-row to §5.1 by the E2E gate, so
  the triangle closes transitively without a second markdown parser.

- **`@mockulus/admin-sdk`**, a TypeScript client for the admin API, in
  `sdk/typescript`. Not published yet; the WireMock-style builders and the test
  helpers follow.

  `MockulusClient` covers the admin surface through namespaces that mirror it —
  `mappings`, `requests`, `nearMisses`, `scenarios`, `files`, `settings`,
  `system` — over the platform's own `fetch`, with **no runtime dependencies**.
  Its request and response types are generated from `api/openapi.yaml`,
  committed, and regenerated-and-diffed in CI, so a type here cannot quietly
  disagree with the contract, and the contract cannot quietly disagree with the
  server.

  Every non-2xx becomes a `MockulusError` carrying **every** problem the server
  reported rather than the first. That matters more here than it looks: mockulus
  collects the whole list before answering, so a mapping with three unsupported
  fields is one round trip, and a client that surfaced only `problems[0]` would
  hand that back three at a time. `pointers()` gives the JSON Pointers to fix,
  and the guards worth naming have names — `isJournalDisabled` for the default
  configuration, `isStoreUnavailable` for the one class here worth retrying.

  The error catalog it exports is held against SPEC Appendix B by the package's
  own tests in both directions, so the codes cannot drift from what the server
  answers.

  The integration suite drives a real mockulus it starts itself, on port 0, with
  the addresses read from the startup line — a client that type-checks against
  the contract can still send something the server refuses, and only a live
  round trip finds that.

- **`AGENTS.md`**, and a `CLAUDE.md` that points at it rather than repeating it.
  It carries the repo's orientation, the rule that a behavior change updates the
  catalog and the corpus in the same PR, and its new sibling: a change to the
  admin surface updates the contract and the SDK in the same PR. It also brings
  the probing discipline into the tree — the rules for establishing what an
  external reference does, each of which has already been paid for once, with
  the incident that bought it written next to it.

- **An embedded admin UI**, served at `/__admin/mockulus/ui/` on both listeners
  and compiled into the binary. This release lands the toolchain and the serving
  contract; the interface itself is thin and grows over the releases that follow.

  It is not part of the WireMock-compatible surface and does not pretend to be.
  It lives in a new reserved namespace, `/__admin/mockulus/**`, specified in
  SPEC §5.7 — WireMock answers 404 for every path under it, so nothing here can
  collide with a client written against WireMock, and an unclaimed path inside
  the namespace answers the same unsupported-endpoint 404 as anywhere else. The
  UI talks only to the public admin API: there are no private endpoints behind
  it and no server-side session state, so everything it can do is something a
  `curl` can do.

  **One security amendment comes with it, and it is worth reading rather than
  skimming.** §17 has always said the admin token guards the whole `/__admin`
  mux, so a route added later is protected by the middleware already in place.
  The UI's static assets are the one exemption. A browser cannot attach an
  `Authorization` header to a page load, or to the script and stylesheet
  requests that page issues, so a token in front of the assets makes the UI
  unreachable in exactly the deployments that set one. What is exempt is code;
  the data behind it is not — the operator types the token into the UI and every
  API call it makes carries it, refused without it like any other. One corpus
  case pins both halves together, because neither means anything alone.

  `ui_enabled` (default `true`) removes the surface entirely when set to
  `false`: the routes stop existing rather than existing and refusing, and the
  admin port's root redirect goes with them.

  A plain `go build` with no Node installed still produces a working binary. The
  repository commits a placeholder rather than a bundle, and a binary built over
  it serves a page saying so and how to build one — a state a contributor can
  read, rather than a link that 404s. Released binaries and container images
  always carry the real UI.

- `matchesJsonSchema`, with its `schemaVersion` modifier. Another 422 becomes a
  supported feature. Drafts `V4`, `V6`, `V7`, `V201909` and `V202012` are
  accepted, defaulting to `V202012`, and the operand may be an inline object, a
  boolean, or a schema encoded as a JSON string — all three spellings WireMock
  takes.

  The part worth knowing before writing one: **the draft decides whether `format`
  does anything.** Under 2019-09 and 2020-12 `format` is an annotation rather
  than an assertion — the JSON Schema specification moved it into a vocabulary
  that is off by default — so on the default draft `{"type":"string",
  "format":"email"}` accepts `"not-an-email"`. Pin `V7` or declare `$schema` in
  the document to get it enforced. A document's own `$schema` overrides
  `schemaVersion` in both directions, which is again WireMock's behaviour and was
  established by probing it.

  `$ref` resolves within the document — `$defs`, `definitions`, JSON pointers,
  `$anchor` and `$id` all work. A reference out of the document is refused at
  registration with the new code `1006` rather than accepted and left to match
  nothing, which is what WireMock does with one: it never fetches the URL either,
  so nothing is lost but the silence. The same refusal covers the other documents
  that are JSON but not usable schemas — a `type` naming no type, a dangling
  `$ref`, an unrecognised `$schema`, and a bare scalar, which on WireMock
  registers happily and then matches **every** request.

  One difference runs the other way, and a suite validating scalar bodies could
  notice it: the matcher here validates the parsed JSON document, so a body that
  is not JSON is a non-match. WireMock falls back to validating the raw request
  text as a JSON string, which makes `{"type":"string"}` and its own negation
  both match the body `4`. Object and array bodies — what schemas are normally
  written against — behave identically on both.

### Changed

- **Three modifier names that do not exist were removed from the stub format.**
  `truncateExpectedTo`, `truncateActualTo` and `expectedOffset` were named in the
  spec, in the roadmap and in the shipped allowlist, and WireMock has none of
  them. The effect was inverted: every real modifier was reported as an unknown
  matcher, and the three invented ones passed without comment. WireMock drops an
  unrecognised key silently, so registering successfully never proved a name
  existed — only reading the stub back does. Because our own documentation is
  the reason anyone would be typing one, all three now answer with the parameter
  that does exist rather than a bare "unknown matcher" — and `expectedOffset`
  says there is no offset parameter at all, since the offset goes into the
  expected value itself.
- SPEC §5.6 no longer promises a `runner --record-wm` flag. It was never built,
  and the section had no catalog entry of any kind, which is how the claim
  survived a whole release; the section is now pinned like §8, §9 and §11 so the
  rest of it cannot go the same way.
- **Three specification errors, all found by writing the contract against a
  running server rather than by reading.**

  Appendix A's annotated example spelled `equalToJson` as
  `{"equalToJson": {"value": "…", "ignoreExtraElements": true}}`. That is not a
  form either server has: the flags are siblings of the operand, not members of
  it, so the criterion compared the body against the literal object
  `{"value": …, "ignoreExtraElements": true}` — it registered cleanly and
  matched nothing anybody would send, while the request the surrounding prose
  described got a 404. It was also not valid JSON, because a `\'` escape inside
  a string is not one, so the example could not have been posted as printed. The
  whole mapping is now registered against a live server and serves the request
  it describes.

  §5.2 had no row for `doesNotMatchJsonPath`, which the server has supported all
  along. A matcher with no row has no catalog entry and so no gate, which is how
  it stayed undocumented; it now shares a row with `matchesJsonPath` the way
  `doesNotMatch` shares one with `matches`, and the evidence contract requires a
  case that proves the negation.

  §11.2's journal-entry example carried a top-level `ts`, a `pod` and a
  `bodyAsBase64`. None of the three is emitted — the first two are storage
  metadata that never reach the document a query returns, and an over-long body
  is flagged with `bodyTruncated` rather than re-encoded. Their absence is what
  a client codes against, so the section now names it.

- The differential harness asks the oracle what it is before deriving a single
  expectation from it. It started the container and read the port back from
  Docker, so nothing should have been able to answer in its place — this
  mechanises a rule rather than closing a live hole. The rule was paid for: a
  stray process once answered on a port taken to be the oracle's, and a batch of
  confident, wrong findings was recorded from it before anyone asked what was
  listening. Every one of them was mockulus agreeing with itself.
- The compatibility matrix is now drift-gated in CI. It is generated from the
  behavior catalog and the corpus precisely so that it cannot claim support the
  gate does not enforce, and that property only holds if the check runs — the
  §13 configuration table and `THIRD_PARTY_LICENSES` were both gated and this
  was not, which left the most public of the three the only one that could go
  quietly out of date.
- The match path carries a context. The scenario state read of §9.2 — the one
  piece of I/O it is allowed to do — used to invent a `context.Background()` at
  the call site, so a client that hung up still had its read run to the full
  `scenario_kv_timeout`. A cancelled request now cancels the read it is waiting
  on. Internal signatures only; the hot-path allocation budget is unchanged.

Tracing is configured only by mockulus' own keys; the standard `OTEL_*`
environment variables are deliberately not read, so one mechanism owns the
generated §13 table, validation, and the redaction that keeps an ingestion token
out of the startup dump. Tracing that is enabled with no collector to export to,
or with an endpoint carrying a scheme `tracing.insecure` already answers, is
refused at startup rather than run.

## [1.0.0] - 2026-07-29

The first release. The compatibility surface is complete, the engine is
finished, and every question the differential harness raised has been answered —
fixed where mockulus was wrong, numbered in SPEC §5.5 where the difference is
deliberate.

One M6 exit criterion is **deliberately deferred rather than met**: §20 asks for
all ten performance scenarios green on the reference rig of §16.1, and that rig —
one pod at 2 vCPU with the load generator on a separate machine — does not exist
yet. The scenarios themselves do, each encoding its SLO as a threshold that
fails the run rather than printing a number for someone to interpret, and the S1
baseline has been reproduced on a developer machine. What is missing is the
hardware to state them against, not the means to measure. This is recorded here
rather than quietly dropped, because a release that claims an exit criterion it
skipped is worse than one that says which.

### Added

- The matching engine of SPEC §6: the immutable snapshot and its RCU swap, the
  candidate indexes, lazy request parsing, the cheap-to-expensive matcher order,
  the RE2/regexp2 seam with Java syntax translation, an in-repo JSONPath
  evaluator, near-miss scoring, and per-element compile quarantine.
- The full stub format of §5.2 and the admin API of §5.1 — mappings CRUD,
  bulk import with its duplicate policies, the files API, settings, reset — with
  the error catalog of Appendix B behind it. Every unsupported field is refused
  at registration with a pointer to the field and a link to the roadmap, and one
  response lists every problem it found rather than the first.
- The Couchbase store of §7: envelope documents, counters, TTLs, the epoch
  poller and resync sweep of §8, the write-path splice and compile cache of
  §4.3/§6.2, and the degraded modes of §4.6.
- Response templating (§10), scenarios with CAS transitions (§9), and the
  request journal with its verification endpoints (§11).
- The `file` store driver, which serves an existing WireMock project directory
  read-only so a team can point mockulus at the tree they already have.
- Documentation under `docs/`: getting started, a WireMock migration guide, a
  configuration reference, an operations guide, the deviation list in usable
  form, and a compatibility matrix generated from the behavior catalog and the
  corpus so it cannot claim support the gate does not enforce.
- The load scenarios S2–S10 of §16.1, each encoding its SLO as a threshold so a
  run that misses the target fails rather than reporting a number to be judged.

### Changed

- SPEC §5.5 grew from 3 deviations to 48. The additions are not new behavior:
  they are behavior the differential corpus found the compatibility claim was
  relying on without recording. Each carries a case pinning it.
- §6.6 now states that Java regex syntax is translated wherever the translation
  is exact, narrowing the RE2-versus-Java carve-out to the residue where no
  exact translation exists.
- The Go module path is settled at `github.com/b3vet/mockulus`, and §15.1's
  image target moves from an estimated 25 MB to a measured 40 MB.
- Appendix C is empty. Every item it tracked has been probed and folded into the
  section that owns it.

### Fixed

Twenty-nine behaviors where mockulus disagreed with pinned WireMock 3.13.2, all
found by the differential corpus rather than by report. The ones a user would
have noticed:

- Templated output was HTML-escaped, which corrupted any templated JSON
  containing `&`, `<`, `>`, `"` or `'`.
- `{{now}}` and `{{randomValue}}` rendered nothing, because a helper called with
  no arguments was parsed as a variable.
- A stub declaring a `Content-Length` smaller than its body served the header
  and then no bytes, so clients blocked or reported a truncated read.
- `equalToJson` compared numbers at float64 precision and therefore matched
  documents it should not have — a stub keyed on a wide numeric id also answered
  for its neighbour.
- A semicolon in a query string dropped the whole parameter, which also made
  `{"absent": true}` match a request that carried it.
- `$.xs.length()` compiled to a lookup for a member named `length()` and
  silently never matched, which §6.7 explicitly forbids.
- A null matcher operand registered and became the empty string, so
  `{"contains": null}` matched every request with a body.
- An upper-case stub id in an admin path did not resolve, though §5.2 says ids
  are parsed case-insensitively.
- A read-only store reported admin writes as "the stub store is unavailable",
  which reads as a Couchbase outage to someone running the file driver.
- A scenario's retry logic classified store errors by matching their text, so a
  scenario named after that text turned any outage into a silent retry loop.
- `binaryEqualTo` with an empty operand could not match an empty body, which
  made it the one body matcher unable to say "the body is empty".
- A mapping id in an admin path that was not a UUID answered a bare 404, which
  reads as "no such stub" for a segment that could never have named one.
- `request.id` echoed an inbound `X-Request-Id`, letting a caller choose what a
  template rendered — and two callers choose the same value.
- A quarantined stub logged how many problems its mapping had and not which
  fields, which for the file driver is the only report there is.

### Testing

- The E2E corpus grew to 537 cases over 213 catalogued behaviors, 353 of them
  replayed against pinned WireMock 3.13.2 on every merge and diffed step by
  step.
- Unit coverage of the §19.2 correctness core — match, matchers, stub, template,
  scenario — meets its 85% bar; three of the five are at 100%.
- The allocation ceilings of §16.3 are asserted outside the race suite, because
  the race detector allocates on its own account and not repeatably.

### Laid down in M0, and still standing

Everything above was built on this, and none of it changed shape on the way to
1.0.0 — which is the argument for having spent M0 on it.

- Repository, build tooling and the open-source governance of SPEC §22:
  Apache-2.0 throughout with an enforced SPDX header check, DCO sign-off,
  CONTRIBUTING encoding the working contract, SECURITY with the threat model,
  Contributor Covenant 2.1, CODEOWNERS, and a license gate that denies copyleft
  in the shipped binary's module graph.
- The typed configuration surface of SPEC §13: every key, bound from environment
  variables and an optional YAML file, with env > file > default precedence,
  exhaustive validation, secret redaction and `_FILE` mounts. The reference
  table in the spec is generated from the struct, and CI fails on drift.
- The serving skeleton of SPEC §4: two listeners with the routing of §12.1, the
  immutable snapshot and RCU swap of §6.2, the store interfaces of §7.1 with an
  in-memory driver, the error catalog of Appendix B, and the startup and drain
  sequences of §4.4 and §4.5. Matching covers method with exact `url` and
  `urlPath`; the rest of the matcher set arrives in M1 and is rejected with a
  422 until then rather than silently ignored.
- The E2E regression gate of SPEC §19: a black-box runner, a behavior catalog
  derived from the spec's own structured blocks with hash-pinned rows and
  machine-checkable evidence contracts, blocking completeness gates, and the
  first corpus. 181 behaviors catalogued.
- The k6 load harness and the S1 baseline of SPEC §16, recorded from the first
  milestone so "fast" stays falsifiable.
- Helm chart and kustomize manifests with probes, PDB, HPA, NetworkPolicy,
  topology spread and a hardened values preset.

- SPEC §13's table began spelling `couchbase.query_timeout` in full rather than
  in shorthand, and documenting port `0` as binding an ephemeral port — both
  fell out of generating the table from the configuration struct rather than
  maintaining it by hand.
