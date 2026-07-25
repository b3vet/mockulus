# The E2E regression gate

This directory is the authoritative gate of the project (SPEC §19). Every
pipeline runs it, and **no artifact — image, chart, binary, tag — ships without
it passing**.

```sh
make e2e                                    # run it
make e2e-catalog                            # gates only, no execution
go run ./test/e2e/runner --run stub-round    # one case, by id substring
```

## What is in here

| Path | What it is |
|---|---|
| `runner/` | The standalone Go runner: spec parsers, catalog lint, case executor, artifacts |
| `catalog/` | The behavior catalog — one entry per externally observable behavior |
| `corpus/` | The cases, as YAML |
| `gotests/` | Cases YAML cannot express: raw-socket assertions, connection-level behavior |
| `topologies/` | Topology definitions for the shapes that need containers |
| `CURRENT_MILESTONE` | The coverage cursor; bumped in the PR that closes a milestone |
| `WIREMOCK_VERSION` | The pinned compatibility oracle |
| `coverage_floor.txt` | The E2E-only coverage floor over `internal/` |

## How "100% of observable behavior" is made falsifiable

The claim would be marketing if nobody could check it, so it is mechanised in
three layers.

**The universe is derived from the spec, not from a wish list.** Parsers in
`runner/spec.go` extract behavior-bearing rows from the spec's own structured
blocks — the §5.1, §5.2, §4.6, §10.3 and §13 tables, the §5.5 deviation list,
the §14.1 collector block, and the Appendix B error catalog. The lint fails when
a row has no catalog entry. Adding a row to the spec therefore *creates work*
rather than silently widening the gap.

Each entry pins a `spec_hash` computed over the extracted behavior tuple — the
trimmed cell text — not the raw Markdown. Reflowing a table or rewording a
heading costs nothing; changing what a row *says* fails the gate until a human
re-reads it and re-syncs.

Behaviors stated in prose cannot be derived this way. The three that matter —
§8 propagation, §9 scenario semantics, §11 journal consistency — live in
`catalog/prose.yaml`, hash-pinned per section and explicitly marked as manually
synced. The "single source of the universe" claim is scoped to exactly the
mechanism above; that file is the deliberate exception.

**Evidence contracts stop vacuous coverage.** A case asserting `status: 200`
must not be able to claim it covers an error code. So every entry carries an
`evidence` sentence *and* `evidence_tokens` — strings that must literally appear
in a binding case's assertions. The gate checks the tokens.

**The completeness gates are blocking:**

- every behavior at or below `CURRENT_MILESTONE` has a passing case satisfying
  its evidence contract — later-milestone entries are catalogued but exempt
  until their milestone lands
- every case references at least one behavior: no orphan tests
- no `pending-dh` entry survives past its own milestone
- every catalog anchor resolves to a real spec heading

A spec row with no distinct observable behavior — a pure tuning knob — may carry
a reviewed `exempt:` instead of an entry. There are three today, each with its
reason written out.

## Writing a case

```yaml
id: stub-roundtrip-001
behaviors: [B-ADMIN-MAPPINGS-POST, B-REQ-URLPATH]
requires: []                 # topology capabilities: couchbase, multi-pod, exclusive
config: default              # instance variant
wm: verified                 # verified | n/a
steps:
  - admin: {method: POST, path: /__admin/mappings, body_file: stubs/hello.json}
    expect: {status: 201}
  - request: {method: GET, path: /e2e/stub-roundtrip-001/hello}
    expect: {status: 200, body: "world"}
```

Rules that keep the suite honest and fast:

- **Namespace your mock traffic** under `/e2e/<case-id>/`. Cases run
  concurrently against shared instances, which dogfoods the shared-deployment
  pattern the docs recommend to users.
- **Never sleep.** Use `expect_eventually` with a `within:` window for anything
  whose contract is eventual. The gate asserts eventual *correctness*; timing is
  asserted only by the perf suite on the reference rig.
- **Declare `requires: [exclusive]`** if the case needs pristine global state —
  global resets, settings, journal-wide queries. Those run serially.
- **Assert something behavior-specific.** If the case exists to cover a
  behavior, it must satisfy that behavior's evidence tokens.

## Topologies and variants

Cases declare what they need; the runner schedules each onto the cheapest
topology that satisfies it.

| ID | Shape | When |
|---|---|---|
| T1 | 1× mockulus, memory store | every PR |
| T2 | 1× mockulus + Couchbase | every PR |
| T3 | 3× mockulus + Couchbase + a round-robin proxy | every PR |
| T4 | kind + the Helm chart | merge and release |
| T5 | mockulus + pinned WireMock, differentially diffed | merge, nightly, release |

Topology shape cannot express start-time configuration, so each topology hosts
named instance variants (`journal`, `authed`, `fast-clock`, `h2c`, `tls`, …).
mockulus starts in well under a second, so variants multiply cheap processes
rather than containers.

## Black-box, with one sanctioned exception

Cases reach mockulus only through public surfaces: the mock port, the admin
port, `/metrics`, `/healthz` and `/readyz`, process lifecycle, and captured
stdout. There are no test hooks in the product.

The exception is `storeprobe`, for behaviors whose only observable *is* the
stored artifact — the document envelope shape, the presence of a TTL. Those
cases are tagged `white-box: true` and kept few.
