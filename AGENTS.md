# Working in this repository

Orientation for anyone — person or agent — making a change to mockulus. This file is
canonical and tool-agnostic; `CLAUDE.md` points here so there is one copy of these rules
rather than two that drift apart. [CONTRIBUTING.md](CONTRIBUTING.md) is the contract a
pull request is held to; where the two overlap they say the same thing on purpose.

## What mockulus is

A mock server wire-compatible with a defined subset of WireMock 3.x — the same admin API
under `/__admin`, the same stub-mapping JSON — rebuilt for horizontal scale: N replicas
behind a Service, stubs persisted in Couchbase, every mock request served from an
in-memory snapshot.

Four principles decide most arguments here (SPEC §2). The hot path does no I/O. Every
expensive feature is pay-per-use, which is why the request journal is off by default where
WireMock has it on. Unsupported input fails loudly — a 422 naming the offending JSON
pointer and pointing at the roadmap — so a stub that registers is a stub that matches,
never one that was accepted and quietly ignored. And the server starts with no
configuration at all.

`SPEC.md` is the authoritative specification: the docs in `docs/` are written from it and
the behavior catalog is parsed out of its tables, so a claim that is not in the spec is not
a claim the project makes. `ROADMAP.md` is what was deliberately left out of v1, which
every unsupported-feature 422 points at.

| Path | What it holds |
|---|---|
| `cmd/mockulus` | the binary's entry point |
| `internal/` | the server; `internal/admin` is the admin API surface, `internal/match` the matching engine and snapshot |
| `internal/{match,matchers,stub,template,scenario}` | the correctness core, held to a higher unit-coverage bar (SPEC §19.2) |
| `test/e2e/` | the regression gate: `runner/` (executor and lints), `catalog/` (behavior catalog), `corpus/` (cases), `gotests/` (cases YAML cannot express) |
| `ui/` | the admin UI; vite builds it into `internal/adminui/dist`, where `go:embed` picks it up |
| `docs/` | the user documentation, written from `SPEC.md` |
| `scripts/` | the checks that are not Go tests — SPDX headers, the coverage floor, npm licenses, the generated compatibility matrix |
| `.github/workflows/` | the pipelines of SPEC §19.5 |

Node is not a build dependency of the server. `make build` works on a machine that has
never installed pnpm; the UI route serves a "built without the admin UI" notice until
`make ui-build` has run. The guarantee that shipped artifacts do carry the UI is enforced
where artifacts are produced — the Dockerfile, goreleaser, and the CI lanes that gate a
binary — rather than by making every Go-only contributor install a toolchain.

## The rule that matters

**A pull request that changes behavior updates the behavior catalog and the E2E corpus in
the same PR.** This is not reviewer vigilance. The completeness gates fail the build when a
spec table row has no catalog entry, when a catalog behavior at or below the milestone
cursor has no passing case satisfying its evidence contract, or when a case references no
behavior at all (SPEC §19.2). Adding a row to the spec creates work rather than silently
widening a gap.

### Its sibling on the admin surface

**A change to the admin surface — routes, the stub-mapping schema, the error catalog,
response envelopes, auth — updates `api/openapi.yaml`, the generated SDK types, and the
affected SDK client, builders and test helpers in the same PR, exactly as a behavior change
updates the catalog and the corpus.** The UI consumes the SDK, so UI-visible surface changes
follow automatically.

The reasoning is the same in both directions. `api/openapi.yaml` is where the TypeScript
SDK's types come from, so an operation missing there is a call the SDK cannot make, and an
operation present there that the server does not implement is a call that compiles and then
404s. Neither is visible by reading either file alone. The contract is the admin surface's
catalog, and the SDK is its corpus.

The generation step exists now, so the rule binds as a command. `pnpm sdk:gen` — or
`make sdk-gen` — regenerates the committed types from `api/openapi.yaml`, and
`pnpm sdk:gen:check` runs that same generation and then `git diff --exit-code` over the
result, which is the drift gate the PR lane runs. It regenerates in place rather than into a
scratch copy, so running it on a dirty tree tells you about your own edit as well: a failure
means the committed types and the contract disagree, whichever of the two moved. Editing the
generated types by hand is not a shortcut; the gate reverts the argument. The rest of the workspace scripts are `build`, `dev`, `check` and `test`, which
delegate to the admin UI, and `sdk:build`, `sdk:check`, `sdk:test` and `sdk:test:integration`,
which delegate to the SDK — `make ui-check` and `make sdk-check` are the Make targets over
them. The integration lane needs a built server on the path and is the only check that proves
the client can still talk to one.

The three-layer pattern is deliberate: the rule is stated here, restated in
`CONTRIBUTING.md`, and enforced mechanically. The mechanical half that exists today is
`make contract-lint`, which cross-checks `api/openapi.yaml` against the behavior catalog in
both directions and runs in the PR lane's SDK job — so a route that reaches the server
without reaching the contract fails CI rather than review. The types drift gate joins it
when the generated types do.

## Running the gates

Everything is a Make target; `make help` lists them all. The Go toolchain version is the
one `go.mod` declares, and Docker is what the image, store and differential topologies need.

```sh
make build            # the binary, into bin/
make test             # unit tests with the race detector
make test-alloc       # the hot-path allocation budgets (no race detector)
make lint             # golangci-lint
make spdx             # every mockulus-authored source file carries its header
make e2e              # the E2E regression gate (builds a binary on demand)
make e2e-catalog      # the catalog and static gates only, executing nothing
make ui-check         # svelte-check, eslint, prettier and vitest for the admin UI
```

One case at a time, by id substring, is the inner loop worth knowing:

```sh
go run ./test/e2e/runner --run stub-round
```

The differential topology (T5) replays every `wm: verified` case against the pinned
WireMock in `test/e2e/WIREMOCK_VERSION` and diffs the answers step by step. It needs
Docker, and CI runs it on merge, nightly and release rather than on every PR:

```sh
go build -o bin/mockulus ./cmd/mockulus
go run ./test/e2e/runner --binary bin/mockulus --parallel 16 --differential
```

Generated artifacts have regeneration targets rather than being edited by hand:
`make config-docs` rewrites the SPEC §13 configuration table from the config struct,
`make compat-docs` rewrites the generated region of `docs/compatibility.md` from the
catalog and the corpus (`make compat-docs-check` verifies without writing), and
`make license-report` regenerates `THIRD_PARTY_LICENSES`, which CI diffs. `make license-check`
and `make npm-license-check` are the two halves of the dependency-license gate.

The PR pipeline (`.github/workflows/pr.yml`) runs lint, unit + race, the admin UI checks,
DCO sign-off, the license gate, the E2E gate on a coverage-instrumented binary, and a
boot/serve/admin smoke against the shippable image. Merge (`main.yml`) re-runs the corpus
on the uninstrumented image and adds T4 (kind and Helm) and the full differential. The E2E
gate is *the* gate: no image, chart, binary or tag ships without it (SPEC §19.5).

## Probing an external reference

Rules for establishing what an external reference does by probing it. They exist because
each one has already been paid for once.

This project's compatibility claim rests on differential observation of a pinned oracle
(SPEC §5.6), so the quality of a probe is the quality of the claim. A probe that is merely
*wrong* is cheap; a probe that is wrong and looks like data is what these rules are about.

**1. Assert the reference's identity before recording anything from it.** Reachability is
not identity. Ask the thing what it is and check the answer: the WireMock oracle must report
the version in `test/e2e/WIREMOCK_VERSION` from `GET /__admin/version`; a Couchbase must
answer `/pools`.

*Paid for on 2026-07-30.* A stray `mockulus` from an earlier test run was bound to the port
a freshly started `wiremock/wiremock:3.13.2` container had published. `curl` succeeded,
`docker ps` said `healthy`, and the first probe batch reported `422 … not supported in
mockulus v1` for every matcher — a plausible WireMock refusal, and in fact mockulus
answering its own probes. Only `/__admin/version` naming mockulus caught it. Nothing
downstream would have.

The harness now does this for itself: `StartWireMock` asks `/__admin/version` before the
first expectation is derived, and refuses a service that answers with mockulus'
`guessedWireMockVersion` field — the tell that resolved the incident. It checks *identity*
rather than matching the reported version against the pinned tag, because an image named by
digest or by a floating alias would fail that comparison for no gain. A probe run by hand
has no such protection and needs the rule.

**2. Bind to an explicit, unusual port.** `-p 127.0.0.1:8089:8080`, not a plausible default.
Instances started with `MOCKULUS_PORT=0` take ephemeral ports, and ephemeral ports land on
plausible defaults. Check first: `lsof -nP -iTCP:<port> -sTCP:LISTEN`.

**3. Never broadly `pkill` to clear the way.** Process names are shared with other projects
and with services the user is running. Identify the specific PID and the specific port. If
something you did not start is in the way, say so and work around it — see
`test/e2e/README.md` on `MOCKULUS_E2E_CB_DIRECT` for the shape of a workaround that does not
require stopping someone else's container.

**4. Pair every positive claim with a probe that would have falsified it.** "It matched" is
not a finding. A matcher that compares correctly and a matcher that accepts everything are
indistinguishable from a single passing probe, and several date-time positions in WireMock
genuinely do accept everything. Run the inverse. If a pair is ambiguous, design a third
probe that separates the hypotheses, and if you cannot, write UNRESOLVED and say what would
settle it.

**5. Registration success is not evidence a key exists.** WireMock silently drops an
unrecognised key inside a matcher document: 201, and the key is absent from
`GET /__admin/mappings`. So a 201 says nothing about whether a parameter name is real.
Round-trip it — register, read back, compare.

*Paid for on 2026-07-30.* `truncateExpectedTo`, `truncateActualTo` and `expectedOffset` were
in SPEC §5.2, ROADMAP 1.2, a plan of record and the shipped `modifierKeys` whitelist. None of
them exists. They registered cleanly for as long as anyone had checked.

**6. One oracle cannot answer a question its configuration decides.** If the answer depends
on the server's environment, one instance cannot separate the hypotheses. Zone resolution is
the example: on a `TZ=UTC` oracle, "a zoneless value is read as UTC" and "read in the
server's default zone" predict identical results on every probe. Start a second oracle in a
different zone and the question collapses in one probe.

**7. Isolate by namespace, never by reset.** Concurrent probes share one oracle. Give each a
distinct URL prefix and **never** call `POST /__admin/reset`, `DELETE /__admin/mappings` or
`POST /__admin/mappings/reset` — that destroys every other prober's work, and the damage
looks like a finding. This is the same shared-deployment discipline SPEC §1 recommends to
users, and the same discipline the corpus dogfoods with its `/e2e/<case-id>/…` prefixes.

The E2E harness resets the oracle between differential cases
(`test/e2e/runner/differential.go`) and is not an exception to this: it starts that
container itself and nothing else is using it. An oracle you did not start is never in that
position.

**8. Tear down what you started.** Leftover containers hold fixed ports, and leftover
processes impersonate oracles (rule 1). Both have cost a debugging session already.
