# Contributing to Mockulus

Thanks for your interest. This document is the working contract: what the
project expects of a change, and what CI will check mechanically.
[AGENTS.md](AGENTS.md) is the orientation that goes with it — the shape of the
repository, the same rules stated for whoever or whatever is working in it, and
the discipline for probing an external reference.

## The one rule that matters

**A pull request that changes behavior MUST update the behavior catalog and the
E2E corpus in the same PR.** This is not reviewer vigilance — the completeness
gates in CI fail the build when a spec table row has no catalog entry, when a
catalog behavior at or below the current milestone has no passing case, or when
a case references no behavior at all (SPEC §19.2).

It has a sibling on the admin surface, which is the same rule with a different
pair of artifacts. **A change to the admin surface — routes, the stub-mapping
schema, the error catalog, envelopes, auth — updates `api/openapi.yaml`, the
generated SDK types, and the affected SDK client, builders and test helpers in
the same PR.** The contract is what the TypeScript SDK's types are generated
from, so an operation missing there is a call the SDK cannot make, and one
present there that the server does not implement is a call that compiles and
then 404s — neither visible by reading either file alone. The UI consumes the
SDK, so UI-visible surface changes follow automatically. The mechanical half is
arriving with the SDK itself: the contract is cross-checked against the behavior
catalog in both directions, and the committed types are regenerated and diffed,
so a surface change that skips them fails the build rather than the review.

`SPEC.md` and `ROADMAP.md` live in this repository and are updated in the same PR
as the behavior they describe. The architecture decision record (SPEC §3) is
public and append-only: decisions are superseded by new rows, never edited away.

## Developer Certificate of Origin

Contributions are accepted under the [DCO](https://developercertificate.org/).
Sign off every commit:

```
git commit -s -m "your message"
```

This adds a `Signed-off-by:` trailer certifying you have the right to submit the
work under the project's license. CI enforces it. We use the DCO rather than a
CLA deliberately — it is the lowest-friction standard for company-initiated open
source.

## Getting set up

You need the Go version `go.mod` declares, and Docker. Node and pnpm are needed only for the admin UI and the TypeScript SDK — the server builds and its Go tests pass without them. Everything else is a Make target.

```sh
make build          # build the binary into bin/
make test           # unit tests with the race detector
make lint           # golangci-lint
make e2e            # the E2E regression gate
make bench          # microbenchmarks with allocation counts
```

Run `make help` for the full list.

## What CI runs on your PR

| Stage | What fails it |
|---|---|
| lint | `golangci-lint`, `gofmt`, SPDX headers |
| unit + race | any failing test, any data race |
| admin UI | `svelte-check`, eslint, prettier, the UI's own unit tests |
| admin SDK | the contract lint against the behavior catalog, `tsc`, eslint, prettier, the SDK's unit tests |
| DCO sign-off | a commit without one |
| license gate | a dependency — Go or npm — outside the allowlist, or a stale `THIRD_PARTY_LICENSES` |
| **E2E gate** | any corpus case, any completeness gate, the coverage floor |
| image + smoke | the shippable image, its instrumented variant, or the smoke run against it |

The E2E gate is *the* merge gate. No artifact — image, chart, binary, tag —
ships without it (SPEC §19.5).

## Writing code here

- **The hot path does no I/O.** Serving a mock request never touches Couchbase,
  disk, or any network dependency. Scenario reads and journal writes are the
  only sanctioned exceptions, and only when those features are in use.
- **Compile at registration, never at serve time.** Regexes, JSONPath
  expressions, templates and response bodies are all resolved when a stub is
  registered or a snapshot is built.
- **Fail loudly.** An unsupported field is a 422 naming its JSON pointer. We
  never accept-and-ignore: a stub that registers successfully behaves as
  WireMock would, within the documented deviations of SPEC §5.5.
- **Keep the dependency surface small.** SPEC §18 carries an allowlist. Adding
  to it needs a discussion in the PR, and the license gate has to stay green.

## Compatibility bugs

If mockulus behaves differently from WireMock for a feature we claim to support,
that is a bug. File it with the compat-bug template, which asks for a
WireMock-versus-mockulus reproduction. Every confirmed compat bug lands together
with a new `wm: verified` corpus case, so it can never regress.

## Reporting security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).

## Code of conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).
