# Contributing to Mockulus

Thanks for your interest. This document is the working contract: what the
project expects of a change, and what CI will check mechanically.

## The one rule that matters

**A pull request that changes behavior MUST update the behavior catalog and the
E2E corpus in the same PR.** This is not reviewer vigilance — the completeness
gates in CI fail the build when a spec table row has no catalog entry, when a
catalog behavior at or below the current milestone has no passing case, or when
a case references no behavior at all (SPEC §19.2).

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

You need Go 1.24 or newer and Docker. Everything else is a Make target.

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
| store integration | Couchbase driver behavior against a real container |
| build image | the shippable image and its instrumented variant |
| **E2E gate** | any corpus case, any completeness gate, the coverage floor |
| license gate | a dependency outside the allowlist |

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
