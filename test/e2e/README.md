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
- **Assert on a global listing by identity, never by position or count.** A case
  shares its instance with every other case, so `GET /__admin/mappings` returns
  their stubs too: the index of your own mapping and the value of `meta.total`
  depend on run order and on which cases happened to be selected. Find your
  mapping by its id with `body_json_contains`, and assert on that.

  ```yaml
  - admin: {method: GET, path: /__admin/mappings}
    expect:
      status: 200
      body_json_contains:
        - path: mappings
          match: {id: "…", response: {status: 200}}
  ```

  The differential diff has the same problem from the other side — the oracle is
  reset per case and holds only your stub, so the two lists cannot agree — and a
  `wm: verified` case declares `wm_ignore: ["$global-listing"]` to have the
  collection compared entry by entry instead of position by position. Every entry
  WireMock listed must still be in yours and match in full; only the order and
  the size stop being claims.

  A case that owns the instance — `requires: [exclusive]`, for the global resets
  and imports whose whole point is the deployment-wide count — may assert the
  envelope exactly. That declaration is what earns it the right to.
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

### When something else already holds Couchbase's ports

T2 and T3 publish 8091, 8092, 8093 and 11210 on the loopback address, and those
numbers cannot be remapped: Couchbase advertises 11210 for KV itself, so a client
handed a remapped host port would still dial 11210. That is also why one machine
runs one gate at a time.

The consequence is that any other container holding one of those ports blocks the
lane, and the answer is not to stop it — it may well be something you need
running. Export `MOCKULUS_E2E_CB_DIRECT=1` and the lane publishes nothing,
addressing the container by its own IP instead:

```sh
MOCKULUS_E2E_CB_DIRECT=1 make e2e
```

It is opt-in because it is only true on some hosts. A container IP is routable
from the host under OrbStack and on Linux, and is not under Docker Desktop's VM
on macOS or Windows, where publishing is the only path that works. CI keeps the
default. If the container reports no address, the runner says so and tells you to
unset the variable rather than failing obscurely.

## Go-native cases

Some behavior is not observable through an HTTP client at all. A fault that
closes the socket mid-body has no status to check, an h2c upgrade is a property
of the connection rather than the exchange, and a drain window only exists
between SIGTERM and process exit. Those live in `gotests/` and talk to the raw
socket and the process.

They count for coverage exactly as a YAML case does, which is the point: if a
Go-native case did not satisfy the catalog, the gate would report a hole where
there is a test, and the pressure would be to write a weaker YAML case instead
of the real one.

`gotests/gotests.yaml` is the join:

```yaml
tests:
  - name: TestFaultEmptyResponse        # the Go test function, matched exactly
    behaviors: [B-RESP-FAULT]
    why: net/http hides a zero-byte close behind a generic transport error
```

Every entry needs a `why`. The escape hatch has to stay deliberate, or it
becomes the easy path.

One rule differs. A YAML case's evidence contract is checked against its
rendered steps; a Go-native case has no steps, so **its evidence is its own
source text** — the catalog's `evidence_tokens` must appear in the test
function. That is a stronger check than the YAML one, since the tokens have to
appear in the code that does the asserting.

The runner hands the binary under test to the suite in `MOCKULUS_E2E_BINARY`,
so `go test ./test/e2e/gotests/` also works on its own.

## Black-box, with one sanctioned exception

Cases reach mockulus only through public surfaces: the mock port, the admin
port, `/metrics`, `/healthz` and `/readyz`, process lifecycle, and captured
stdout. There are no test hooks in the product.

The exception is `storeprobe`, for behaviors whose only observable *is* the
stored artifact — the document envelope shape, the presence of a TTL. Those
cases are tagged `white-box: true` and kept few.
