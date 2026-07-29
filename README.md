# Mockulus

**Mock + Cumulus.** A cloud-native, high-throughput mock server, wire-compatible with a defined subset of the
[WireMock](https://wiremock.org) 3.x admin API and stub format — built for Kubernetes:
N replicas behind a Service, stubs persisted in Couchbase, all mock traffic served from an
in-memory snapshot at ≥50k RPS per 2-vCPU pod with sub-2 ms p99.

**Status: in development.** The core is being built milestone by milestone against
the plan in SPEC §20; nothing is released yet.

## Documentation

Start at **[docs/](docs/README.md)**.

- **[Getting started](docs/getting-started.md)** — install, start with no configuration,
  register a stub, and see what a refusal looks like.
- **[Migrating from WireMock](docs/migrating-from-wiremock.md)** — what moves, what changes
  shape, what to turn on.
- **[Compatibility matrix](docs/compatibility.md)** — every catalogued behavior and the test
  that proves it, generated from the catalog and the corpus.
- **[Deviations](docs/deviations.md)** · **[Configuration](docs/configuration.md)** ·
  **[Operations](docs/operations.md)**

Deeper: **[SPEC.md](SPEC.md)** is the authoritative specification — the docs above are written
from it, and the behavior catalog is parsed out of it. **[ROADMAP.md](ROADMAP.md)** is what was
deliberately left out of v1, which every unsupported-feature 422 points at.
**[test/e2e/README.md](test/e2e/README.md)** is how the gate works.

```sh
make build && ./bin/mockulus      # zero-config: memory store, ports 8080 and 9090
make test                         # unit tests with the race detector
make e2e                          # the regression gate
```

## Regression truth

The in-repo E2E harness (SPEC §19) is the authoritative gate for every pipeline: a black-box
suite executed against a real, started mockulus instance across five topologies (in-memory,
Couchbase-backed, 3-replica cluster, kind/Helm, and differential against pinned WireMock).
A CI-enforced behavior catalog derived from the spec's own tables guarantees 100% coverage of
the defined contract — no artifact ships without it.

## Why not WireMock?

WireMock is excellent, but it is a single-node stateful process: stubs, scenario state, and the
request journal live in one JVM's memory, matching is a linear scan, and the journal is on by
default — it cannot scale horizontally and folds under heavy load. Mockulus keeps WireMock's
API and working logic for a strict subset of features, and rebuilds the runtime around four
principles (SPEC §2): the hot path does no I/O, every expensive feature is pay-per-use, unsupported
input fails loudly (422), and everything runs zero-config first.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The short version: commits are signed off (DCO), and a
change to behavior updates the behavior catalog and the E2E corpus in the same PR — CI enforces
that mechanically rather than leaving it to reviewer vigilance.

## License

[Apache License 2.0](LICENSE). Mockulus is not affiliated with, endorsed by, or sponsored by
the WireMock project or WireMock Inc.; the WireMock name is used solely to describe API
compatibility.
