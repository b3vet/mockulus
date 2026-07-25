# Mockulus

**Mock + Cumulus.** A cloud-native, high-throughput mock server, wire-compatible with a defined subset of the
[WireMock](https://wiremock.org) 3.x admin API and stub format — built for Kubernetes:
N replicas behind a Service, stubs persisted in Couchbase, all mock traffic served from an
in-memory snapshot at ≥50k RPS per 2-vCPU pod with sub-2 ms p99.

**Status: pre-implementation.** The project currently consists of its design documents:

- **[SPEC.md](SPEC.md)** — the authoritative technical specification and implementation plan
  (architecture, compatibility contract, data model, performance SLOs, E2E gate, milestones M0–M6).
- **[ROADMAP.md](ROADMAP.md)** — features deliberately deferred from v1, with design sketches,
  and explicit non-goals.

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

## License

[Apache License 2.0](LICENSE). Mockulus is not affiliated with, endorsed by, or sponsored by
the WireMock project or WireMock Inc.; the WireMock name is used solely to describe API
compatibility.
