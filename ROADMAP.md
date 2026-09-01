# Mockulus — Roadmap & Deferred Features

Companion to [SPEC.md](SPEC.md). Everything here was **deliberately excluded from v1** (decision
D5, 2026-07-22) to ship the performance core first. Each entry records what it is, why it was
deferred, and roughly what it would cost — so a decision to pick one up starts from what was
already thought about rather than from scratch.

A feature still listed as deferred **fails loudly**: a stub using it is rejected with 422 and a
pointer to this document, never silently ignored, so an adopting team learns at registration rather
than from a mock that quietly matched the wrong thing.

**Entries that have shipped are marked, not deleted, and their numbers never move.** The numbers are
cited from `SPEC.md`, `AGENTS.md`, the CHANGELOG and from comments in shipped code — `internal/match/poller.go`
names 2.4 — which makes them an interface rather than a table of contents. Treat a number as an
opaque identifier: it says when an entry was written and nothing about what kind of work it is.
Entries added in the 2026-09-01 re-frame are numbered 4.x for the same reason, continuing the
sequence rather than renumbering anything.

---

## Where this stands — 2026-09-01, after v1.3.1

**The compatibility work is done.** Every v1.x gap with known demand shipped across v1.1.0 through
v1.3.0. Excluding record & playback, which is deferred by decision below, and `customMatcher`, which
is a non-goal, the entire remaining WireMock surface is **proxy mode (2.1)** and **webhooks (2.3)**.

**The differentiator is scale, and it is the one claim not verified.** SPEC §16.1 publishes ten SLOs
as release criteria for v1.0. One of them has ever produced a trustworthy number. That is the
subject of theme A, and it is deliberately first.

This document was re-framed on 2026-09-01. It used to sort entries by implementation size — "v1.x
candidates", "v2 features", "platform" — which stopped being useful once the first of those emptied,
and which never surfaced the scale work because it was scattered across all three. It now sorts by
what the work is *for*.

---

## Theme A — Making the scale claim true

Nothing else in this document matters as much. A mock server whose selling point is that it survives
load, and which cannot demonstrate that it survives load, is asking to be taken on faith.

One piece of this landed in v1.3.1: SPEC §16.2's benchmark tracking, which had been specified since
v1.0 and implemented by nothing. It is now the `bench` job and `make bench-compare`, and on its first
run it caught the regression that prompted it. What remains is the load half.

### 4.1 A reference rig, or an honest §16.1

- **The problem, stated plainly.** SPEC §16.1 lists S1–S10 as "release criteria for v1.0, measured on
  the reference rig". `test/load/BASELINE.md` records that the reference rig is "not recorded, and not
  currently recordable". S1 has one number, taken on a developer laptop at M0 and explicitly marked as
  a floor rather than a ceiling. S2 through S10 have none. Three releases have been cut against
  criteria nobody measured.
- **Why it stayed that way**: the nightly perf job falls through to a shared two-core GitHub runner
  because `PERF_RUNNER` is unset, and there k6 and mockulus compete for the same cores — S1 has read
  `p(99)=75 ms` against a 2 ms target. That failure is a statement about the runner. The job has been
  red for so long that it reports nothing, which is how a genuine UI regression hid in the same
  nightly for days in August.
- **What**: either a rig that can produce the numbers, or a §16.1 that claims only what has been
  measured. Both are acceptable; the current state — publishing ten unverified criteria — is not.
- **Sketch**: the cheap version is one always-on machine (a small cloud VM or a spare box) registered
  as a self-hosted runner and named in `PERF_RUNNER`, with k6 driven from a second machine or a
  second nodepool, since `BASELINE.md` records the same build reading 1.07 ms and 5.03 ms depending
  only on what else the host was doing. The honest-retreat version is smaller: re-mark S2–S10 as
  targets rather than criteria, record what a single-host run does measure, and say in §16.1 that
  the rest is unverified. **Neither is difficult. The decision is what has been missing.**
- **Note on macOS**: a compose rig on a Mac tops out near 19.6k RPS whatever the server does, because
  published ports cross the VM boundary through a userspace proxy. It is a correctness rig, not a
  measurement one, and `BASELINE.md` forbids copying a number out of it.
- **Depends on**: a decision, then a machine. **Size**: S for the retreat, M for the rig.

---

## Theme B — Fidelity under load

This theme is new, and it is where mockulus can do things WireMock structurally cannot. A mock that
stands in for a dependency during a load test is part of the experiment: if it answers unrealistically,
the experiment measures a system that does not exist.

### 4.2 Response-time profiles

- **What**: a stub, or a whole deployment, that answers with a *distribution* rather than a constant —
  "behave like a dependency whose p50 is 20 ms, p99 is 400 ms, with a 0.5% tail at 2 s". Configured per
  route or globally, mockulus-namespaced.
- **Why it matters more than it looks**: a mock answering in 30 µs makes a load test lie. The system
  under test never experiences the connection-pool pressure, the queueing, or the timeout paths that
  the real dependency would cause, so the run measures a best case that will never occur in
  production. WireMock has `fixedDelay` and a uniform/lognormal `delayDistribution`, which is closer
  than nothing but is per-stub and shaped by parameters rather than by the percentiles anyone actually
  has from their own monitoring.
- **Sketch**: a delay sampler on the response pipeline, seeded per deployment for reproducibility, fed
  either by named percentiles or by a recorded histogram uploaded through the files API. The hot-path
  rule is that a deployment not using it pays a nil check. Sleeping must not consume a thread —
  timer-based release, not a blocked goroutine, or S1's throughput target dies the moment anyone
  turns it on.
- **Relation to 3.7**: 3.7 called this "chaos". That framing is wrong and is why it sat unstarted:
  this is not fault injection, it is fidelity. The failure-injection half of 3.7 stays there.
- **Depends on**: nothing hard. **Size**: M.

### 4.3 Bottleneck attribution — "was the mock the constraint?"

- **What**: an answer, in the mock's own numbers, to the question every load test eventually raises —
  did the stand-in dependency distort the result? Concretely: served-request service time as a
  histogram separate from queue/wait time, saturation signals (accept queue depth, in-flight
  requests against capacity), and a plain verdict surfaced in `/__admin/mockulus/**` for the window a
  test ran over.
- **Why**: this is the question your own perf runs ask, and today it can only be answered by
  correlating mockulus's Prometheus series against a k6 report by hand and hoping the clocks agree.
  When a run comes back slow, "was it us or the mock?" is the first thing asked and the most expensive
  thing to answer. WireMock cannot answer it at all.
- **Sketch**: the metrics are largely present (§14.1); what is missing is the separation of service
  time from wait time, and something that reads the two together over a window and says whether the
  mock was near saturation while the test ran. Deliberately a report rather than an alert — it
  informs a human reading a test result.
- **Trigger for costing it properly**: this wants 4.1 first. A verdict about saturation is only worth
  as much as the capacity number it is measured against.
- **Depends on**: 4.1 for the capacity baseline. **Size**: M.

### 4.4 Overload behaviour

- **What**: defined behaviour past capacity — shed with 503 and a metric, or queue with a bounded
  wait, chosen by configuration rather than by whatever the runtime happens to do.
- **Why**: the SLOs describe behaviour *at* a load. None of them describes what happens at twice it,
  which is the condition a load test is specifically trying to reach. Undefined overload behaviour
  makes the far end of every ramp uninterpretable: a latency cliff could be the mock queueing, and
  nothing distinguishes that from the system under test degrading.
- **Sketch**: the journal already has the shape to copy — a bounded queue that drops and counts
  rather than blocking, because P1 says never block the hot path. The same policy applied to the
  accept path, with the choice exposed as configuration and the drop counted.
- **Depends on**: 4.1, to know where capacity is. **Size**: M.

### 3.7 Chaos & fault injection

- **What**: per-route error-rate injection and bandwidth throttling, applied globally or per selector,
  for resilience game-days.
- **Changed 2026-09-01**: the latency-shaping half of this entry moved to 4.2, where it is framed as
  fidelity rather than chaos. What stays here is genuine fault injection.
- **Sketch**: response-pipeline middlewares configured through a mockulus-namespaced settings
  extension, kept out of the WM-compat surface per D2.
- **Size**: M, reduced from its original scope.

---

## Theme C — Cluster behaviour at scale

### 2.4 DCP-based sync (instant propagation)

- **What**: replace or augment epoch polling with Couchbase DCP streaming (e.g. `Trendyol/go-dcp`)
  for near-zero stub propagation latency.
- **Why deferred**: epoch polling meets test-setup semantics at a fraction of the operational
  complexity — rebalance handling, stream state, failure modes.
- **Why it is worth revisiting**: propagation is ~1.5 s at defaults, and it is the delay a test author
  actually feels — register a stub on one pod, and the request that follows may reach another. It is
  the most user-visible number in the cluster story.
- **Sketch**: v1 already isolates the trigger behind the `ChangeSignal` interface (§8), so a DCP
  signaler is a drop-in that marks the snapshot dirty on any mutation in `mappings`.
- **Depends on**: nothing — the interface exists. **Size**: M, mostly ops hardening.

### 2.5 Delta snapshot rebuilds & matcher index v2

- **What**: (a) delta reloads — apply remote changes without a full `LoadAll` on every epoch change;
  (b) a radix/prefix-bucket index over pattern stubs for very large stub sets.
- **Why deferred**: v1 already splices admin writes locally and reuses a compile cache on reloads
  (§4.3/§6.2), so the remaining cost is narrower than it looks.
- **Trigger, made concrete 2026-09-01**: this entry has always said it "depends on production
  profiling evidence", which was an open-ended deferral because there is no production to profile.
  The concrete version: pick it up when the `bench` job shows `BuildSnapshot` or `Rebuild/cold`
  dominating at 10k stubs, or when 4.1's rig shows S7 missing its reload target. Both are now
  measurable; neither has been measured.
- **Sketch**: (a) fetch only docs changed since the last epoch — per-doc CAS or mutation-token
  comparison, or DCP once 2.4 lands — and patch the snapshot; (b) group pattern stubs by literal
  prefix so a candidate set is narrowed before any pattern runs.
- **Depends on**: evidence from 4.1 or the bench job. **Size**: M each.

### 4.5 Rollout and cold-start behaviour at replica count

- **What**: what happens to the store when a deployment of N replicas restarts at once. Each new pod
  does a full `LoadAll`, so a rolling restart of a large deployment is a synchronised read storm
  against Couchbase, at exactly the moment every pod is also failing readiness.
- **Why it is not covered**: S6 measures cold start for *one* pod against a warm store (< 5 s, 10k
  stubs). Nothing measures ten pods starting together, and the interaction — where each pod's own
  load slows every other pod's — is the kind that only appears at replica count.
- **Sketch**: measure first, on 4.1's rig or in the T4 kind lane, before deciding whether anything is
  needed. If something is: staggered start, a shared warm snapshot, or delta loads from 2.5.
- **Depends on**: 4.1. **Size**: S to measure, unknown to fix — which is the honest answer until it
  is measured.

### 3.4 Multi-tenancy

- **What**: many logical mock spaces in one deployment — tenant to scope mapping, per-tenant reset,
  auth and quotas.
- **Why deferred**: D12 — namespaces and deployment-per-team give isolation with zero code. Revisit
  only if platform-team consolidation demands it.
- **Sketch**: tenant resolved from an admin path prefix or header into a per-tenant snapshot map, with
  a per-tenant epoch. The real cost is quota and blast-radius management, not routing.
- **Size**: L.

### 3.5 Local-state performance mode — mostly shipped in v1.2.0

- **Status**: the substance shipped as `profile: local`, presetting `store: memory` and
  `journal_enabled: true` for a laptop or single-pod CI run. What remains is the guard the entry
  asked for: refusing to start, or at least warning loudly, when the profile is set and the
  deployment has more than one replica — where in-process scenario state silently means each replica
  disagrees about it.
- **Remaining size**: XS.

---

## Theme D — Remaining WireMock surface

Two entries, and both introduce an outbound HTTP subsystem this server does not currently have.
That shared cost is the main argument for doing them together, and the main argument against doing
either casually: a proxied or webhooked request performs outbound I/O on a path that principle P1
says does no I/O at all.

### 2.1 Proxy mode (`proxyBaseUrl`)

- **What**: stubs that forward to a real backend for partial mocking and gradual migration, with
  `additionalProxyRequestHeaders`, `proxyUrlPrefixToRemove` and low-priority catch-all forwarding.
- **Why deferred**: introduces an outbound HTTP client subsystem — pooling, timeouts, streaming,
  hop-by-hop header hygiene, retry policy, TLS options — a different risk profile from everything in
  v1.
- **Worth weighing before picking it up**: proxy is the foundation record & playback sits on, and
  record & playback is deferred by choice (below). Building 2.1 alone is defensible — pass-through is
  useful without capture — but a large part of its value is unlocking a feature nobody has asked for.
- **Sketch**: `internal/proxy` with a shared tuned `http.Transport`; the response definition compiles
  to a `ProxyAction`; streaming in both directions, exempt from the body cap.
- **Depends on**: nothing hard. **Size**: M.

### 2.3 Webhooks / `postServeActions`

- **What**: fire templated async HTTP calls after a stub is served, for callback and async-flow
  simulation, with fixed and random delays.
- **Why deferred**: an outbound execution subsystem with its own failure semantics — retries,
  timeouts, at-most-once versus at-least-once — which must not endanger hot-path SLOs.
- **Sketch**: bounded work queue and worker pool, dropping and counting on overflow exactly as the
  journal does; templates reuse the §10 engine over the serve-event model; per-deployment egress
  allowlist.
- **Depends on**: templating, done in v1. **Size**: M.

### 2.2 Record & playback (`/__admin/recordings/*`)

- **Deferred by decision, 2026-09-01.** Not on cost grounds — it is genuinely large — but because it
  serves a use this tool is not aimed at. Mockulus is for standing in for a dependency under load;
  capturing traffic to author stubs is a different job, and WireMock does it well for teams who need
  it. Revisit only on explicit demand.
- **What**: capture proxied traffic and generate stub mappings — snapshot and record modes,
  request-body criteria extraction, dedupe.
- **Depends on**: 2.1, journal. **Size**: L.

---

## Theme E — New protocol surface

### 3.6 gRPC mocking

- **What**: WireMock's gRPC extension equivalent — proto-described services, message matching.
- **Why deferred**: a different protocol surface entirely, and demand is unproven internally.
- **Sketch**: separate listener, proto descriptors uploaded through the files API, matchers over
  decoded messages reusing the JSON matcher tree via protojson.
- **Size**: L.

---

## Shipped

Kept for their numbers, which are cited elsewhere. Detail lives in the CHANGELOG and SPEC.

| # | Entry | Shipped | Worth knowing |
|---|---|---|---|
| 1.1 | XML & XPath matching | v1.3.0 | Two sketch claims were wrong: there is **no** precomputed canonical form, because children pair by element name and no canonical serialisation expresses that; and the XMLUnit placeholders are a **non-goal**, not a stretch — WireMock does not interpret them. `formatXml` needed a printer written from recorded oracle bytes. Deviations #59–#62 |
| 1.2 | Date/time matchers | v1.1.0 | The expected value's *type* selects the comparison mode |
| 1.3 | `equalToJson` placeholders | v1 | Never actually deferred — WM interprets them by default, so parity required v1 to |
| 1.4 | `matchesJsonSchema` | v1.1.0 | Drafts V4–2020-12; `format` asserted only under V4/V6/V7 |
| 1.5 | Multipart + multi-value operators | v1.2.0 / v1.3.0 | The parse is memoized on the **body subject**, not `ParsedRequest` as sketched. Elements are ANDed while `matchingType` quantifies over parts; a body with no parts never matches. Deviation #63 honours `name`, which WireMock ignores — the riskiest call in v1.3.0 |
| 1.6 | `host`, `port`, `scheme` | v1.3.0 | An oversight, not a decision — refused by name with no roadmap entry costing them. `scheme` reports what *this process* terminated; forwarding headers are deliberately not consulted. Made lazy in v1.3.1 after they were found to cost every request |
| 3.1 | Admin UI | v1.1.0 | At `/__admin/mockulus/ui/`, embedded in the binary, talking only to the public admin API |
| 3.2 | OpenTelemetry tracing | v1.1.0 | Off by default; `tracing.*` keys only, `OTEL_*` deliberately not read |

---

## Rejected

### 3.3 Migration & tooling CLI (`mockulusctl`) — rejected in v1.2.0, superseded

The value of this entry was concentrated in `validate` — a dry-run report letting a team assess a
migration before deploying anything — and that shipped as `POST /__admin/mockulus/validate` (SPEC
§5.7.2) instead. A second binary to install, version and document earned nothing the endpoint does
not.

### Explicit non-goals — rejected, not deferred

| Item | Why |
|---|---|
| Browser/forward MITM proxying | Different product; conflicts with the in-cluster service model |
| Java-class extensions (`extensions`, custom matchers/transformers as code) | No JVM; arbitrary code in the serving pod breaks the security posture |
| Embedded-library mode (in-process mock for unit tests) | Mockulus is a service; WireMock itself remains excellent for in-JVM unit-test use |
| Bit-identical near-miss diagnostics | Diagnostic text is out of the strict-compat surface (SPEC §6.8) |
| Running as a stateful singleton with in-memory-only durability in production | The entire point of the project is the opposite |

---

## Versioning & compat promise going forward

- v1.x additions must not change the behavior of any stub that registers successfully today — 422
  becoming supported is the only allowed transition.
- The differential harness corpus is append-only; every roadmap feature lands with its corpus cases
  first, spec-first and WM-verified.
- Mockulus-specific API extensions — fidelity, chaos, tenancy, UI, validation — live under
  `/__admin/mockulus/**`. The WM-compatible surface stays a strict mirror.
- Priorities here are a proposal, not a commitment. The 422 codes are counted by
  `mockulus_admin_requests_total`, so demand for a deferred feature is measurable rather than argued.
