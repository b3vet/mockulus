# Mockulus — Roadmap & Deferred Features

Companion to [SPEC.md](SPEC.md). Everything here was **deliberately excluded from v1** (decision D5, 2026-07-22) to ship the performance core first. Each entry records what it is, why it was deferred, a design sketch consistent with the v1 architecture, what it depends on, and a size estimate (S ≈ days, M ≈ 1–2 weeks, L ≈ 3+ weeks).

In v1, every feature below **fails loudly**: stubs using it are rejected with 422 + a pointer to this document (never silently ignored) — so adopting teams always know exactly where they stand.

Buckets are an ordering proposal, not a commitment; reprioritize on demand signal (the 422 error codes are counted by `mockulus_admin_requests_total`, so demand is measurable).

---

## Bucket 1 — v1.x candidates (compat gaps with known demand)

### 1.1 XML & XPath matching (`equalToXml`, `matchesXPath`)
- **What**: structural XML equality (whitespace/attribute-order insensitive) and XPath 1.0 matching, incl. namespaces and WM's `xPath` sub-matcher form; XMLUnit-style ignore placeholders as a stretch.
- **Why deferred**: bounded but non-trivial (canonicalization corner cases); v1 focused on JSON-first traffic.
- **Sketch**: pure-Go via `antchfx/xmlquery` + `antchfx/xpath`; canonical form computed at compile time for the expected document; same compile-at-registration discipline (P1/P2 hold — XML parse of request body is lazy and memoized on `ParsedRequest` like JSON). New matchers slot into `internal/matchers` with zero engine changes.
- **Depends on**: differential corpus expansion for XML cases. **Size**: M.

### 1.2 Date/time matchers (`before`, `after`, `equalToDateTime`)
- **What**: WM 3 temporal matchers with `truncateExpectedTo`/`truncateActualTo`, `expectedOffset`.
- **Sketch**: compile expected instant/offset at registration; parse actual per WM's accepted formats. Pure matcher addition.
- **Depends on**: nothing. **Size**: S.

### 1.3 `equalToJson` placeholders (json-unit) — shipped in v1
- **Status**: implemented during M1, not deferred after all. This entry predates the Appendix C probe that showed WM interprets the placeholders **by default**, so parity required v1 to as well. The supported set (`ignore`, `ignore-element`, `any-string`, `any-number`, `any-boolean`, `regex`) and its semantics are recorded in SPEC §5.2 (`equalToJson` row) and deviations #5 (an *unrecognised* placeholder is refused at registration — the inverse of the compare-literally behavior this entry used to describe) and #25; pinned by the `matchers-json-*` corpus cases. Entry retained under its number so existing references stay valid.

### 1.4 `matchesJsonSchema`
- **What**: validate request body against an embedded JSON Schema (WM 3.3+).
- **Sketch**: `santhosh-tekuri/jsonschema` (drafts 4–2020-12), schema compiled at registration.
- **Depends on**: nothing. **Size**: S.

### 1.5 Multipart matching + extended multi-value operators
- **What**: `multipartPatterns`; `havingExactly`/`includes` multi-value query/header operators.
- **Sketch**: `mime/multipart` lazy parse memoized on `ParsedRequest`; multi-value ops as new `KeyMatcher` modes.
- **Depends on**: nothing. **Size**: S/M.

---

## Bucket 2 — v2 features (new subsystems)

### 2.1 Proxy mode (`proxyBaseUrl`)
- **What**: stubs that forward the request to a real backend (partial mocking / gradual migration), with `additionalProxyRequestHeaders`, `proxyUrlPrefixToRemove`, low-priority catch-all passthrough.
- **Why deferred**: introduces an outbound HTTP client subsystem (pooling, timeouts, streaming, hop-by-hop header hygiene, retry/no-retry policy, TLS options) — a different risk class than serving from memory.
- **Sketch**: `internal/proxy` with a shared tuned `http.Transport`; response definition compiles to a `ProxyAction`; streaming both directions (no buffering — exempt from `max_body_bytes` on the response side); per-stub timeout override; circuit-breaker metric. Journal (when enabled) records proxied exchanges — which is the foundation for recording (2.2).
- **Depends on**: nothing hard; journal integration for capture. **Size**: M.

### 2.2 Record & playback (`/__admin/recordings/*`)
- **What**: capture proxied traffic and generate stub mappings (snapshot + record modes, request-body criteria extraction, dedupe).
- **Why deferred**: largest deferred surface; sits entirely on top of proxy + journal + stub generation heuristics.
- **Sketch**: recorder consumes the journal stream of proxied exchanges; generation rules (URL → equalTo, JSON bodies → equalToJson with sensible flags, id extraction) ported from WM behavior via differential corpus; output goes through the standard admin create path (so validation/422 catalog applies).
- **Depends on**: 2.1, journal. **Size**: L.

### 2.3 Webhooks / `postServeActions`
- **What**: fire templated async HTTP calls after a stub is served (callback/async-flow simulation), with fixed/random delays.
- **Why deferred**: outbound execution subsystem with its own failure semantics (retries, timeouts, at-most-once vs at-least-once) — must not endanger hot-path SLOs.
- **Sketch**: bounded work queue + worker pool (drop+metric on overflow, mirroring journal policy); templates reuse §10 engine with the serve-event model; per-deployment egress allowlist config as a safety rail.
- **Depends on**: templating (done in v1). **Size**: M.

### 2.4 DCP-based sync (instant propagation)
- **What**: replace/augment epoch polling with Couchbase DCP streaming (e.g. `Trendyol/go-dcp`) for near-zero stub propagation latency.
- **Why deferred**: epoch polling meets test-setup semantics at a fraction of the operational complexity (rebalance handling, stream state, failure modes).
- **Sketch**: v1 already isolates the trigger behind the `ChangeSignal` interface (§8); a DCP signaler is a drop-in that marks the snapshot dirty on any mutation in `mappings`/`files`. Keep the resync sweep as backstop. Config: `sync_mode: poll | dcp`.
- **Depends on**: nothing (interface exists). **Size**: M (mostly ops hardening).

### 2.5 Delta snapshot rebuilds & matcher index v2
- **What**: (a) delta reloads — apply remote changes without a full `LoadAll` round-trip on every epoch change; (b) radix/prefix-bucket index over pattern stubs for very large stub sets.
- **Why deferred**: v1 already splices admin writes locally (zero-staleness without recompile) and reuses a compile cache on reloads (SPEC §4.3/§6.2), so the remaining cost is only the `LoadAll` fetch itself; the linear pattern scan is prefiltered. S2/S7/S10 gates decide if either upgrade is ever needed.
- **Sketch**: (a) fetch only docs changed since the last epoch (per-doc CAS/mutation-token comparison, or DCP once 2.4 lands) and patch the snapshot; (b) group pattern stubs by `LiteralPrefix` first segment into a radix tree; both preserve selection-order semantics (§5.3).
- **Depends on**: production profiling evidence. **Size**: M each.

---

## Bucket 3 — Platform & operability

### 3.1 Admin UI
- **What**: read/write web UI (stub browser/editor, journal viewer, scenario states, near-miss debugger). WireMock OSS has none — this is a differentiator.
- **Sketch**: static SPA served from the admin port (embedded via `go:embed`), talking only to the public admin API (dogfooding); no server-side session state.
- **Depends on**: stable admin API. **Size**: L.

### 3.2 OpenTelemetry tracing
- **What**: optional traces for mock requests (match decision, scenario I/O, template render spans) and admin ops; W3C context propagation.
- **Sketch**: `otelhttp`-style middleware, sampled, off by default; hot-path guard: zero cost when disabled (nil-check pattern, no always-on spans).
- **Size**: S/M.

### 3.3 Migration & tooling CLI (`mockulusctl`)
- **What**: one-shot commands: import a WireMock `mappings/` dir into Couchbase, export back, validate a stub corpus against the v1 support matrix (dry-run 422 report — lets teams assess migration before deploying), diff two deployments.
- **Sketch**: same binary, subcommands; reuses `internal/stub` validation and the store drivers.
- **Size**: S/M.

### 3.4 Multi-tenancy
- **What**: many logical mock spaces in one deployment (tenant → scope mapping, per-tenant reset/auth/quotas).
- **Why deferred**: D12 — namespaces + deployment-per-team give isolation with zero code; revisit only if platform-team consolidation demands it.
- **Sketch**: tenant resolved from admin path prefix or header → per-tenant snapshot map; per-tenant epoch. Real cost is quota/blast-radius management, not routing.
- **Size**: L.

### 3.5 Local-state performance mode
- **What**: explicit single-replica mode where scenario state and journal are in-process (no CB on the request path at all) — for laptop dev and single-pod CI, with WM-identical immediacy.
- **Sketch**: memory driver already implements the interfaces; add a config preset (`profile: local`) and docs. Guard: refuses to start with `replicas>1` hint metric/log.
- **Size**: S.

### 3.6 gRPC mocking
- **What**: WM's gRPC extension equivalent (proto-described services, message matching).
- **Why deferred**: different protocol surface entirely; demand unproven internally.
- **Sketch**: separate listener, proto descriptors uploaded via files API, matchers over decoded messages reusing JSON matcher tree (protojson). **Size**: L.

### 3.7 Chaos & bandwidth shaping
- **What**: beyond WM parity: bandwidth throttling, per-route error-rate injection, latency profiles applied globally or per-selector (useful for resilience game-days).
- **Sketch**: response-pipeline middlewares configured via a mockulus-namespaced settings extension (kept out of the WM-compat surface per D2).
- **Size**: M.

---

## Explicit non-goals (not deferred — rejected)

| Item | Why |
|---|---|
| Browser/forward MITM proxying | Different product; conflicts with the in-cluster service model |
| Java-class extensions (`extensions`, custom matchers/transformers as code) | No JVM; arbitrary code in the serving pod breaks the security posture. If extensibility is ever needed, it will be a sandboxed mechanism (e.g. WASM or CEL expressions) designed on its own merits |
| Embedded-library mode (in-process mock for unit tests) | mockulus is a service; WireMock itself remains excellent for in-JVM unit-test use |
| Bit-identical near-miss diagnostics | Diagnostic text is out of the strict-compat surface (SPEC §6.8) |
| Running as a stateful singleton with in-memory-only durability in production | The entire point of the project is the opposite |

---

## Versioning & compat promise going forward

- v1.x additions must not change the behavior of any stub that registers successfully today (422 → supported is the only allowed transition).
- The differential harness corpus is append-only; every roadmap feature lands with its corpus cases first (spec-first, WM-verified).
- mockulus-specific API extensions (chaos, tenancy, UI endpoints) live under `/__admin/mockulus/**` — the WM-compatible surface stays a strict mirror.
