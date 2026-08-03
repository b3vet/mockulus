# Mockulus documentation

**Mock + Cumulus.** A cloud-native, high-throughput mock server, wire-compatible
with a defined subset of the [WireMock](https://wiremock.org) 3.x admin API and
stub format. N replicas behind a Kubernetes Service, stubs persisted in
Couchbase, every mock request served from an in-memory snapshot.

The one property to carry into everything below: mockulus implements a
**subset**, and anything outside it **fails loudly**. An unsupported stub field
is refused at registration with a 422 naming the field; an unsupported admin
path is a 404 with a code and a pointer to the roadmap. Nothing is accepted and
quietly ignored, so a mapping that registers is a mapping that matches — you
find out about a gap when you load your stubs rather than when a test fails at
three in the morning.

## Start here

| | |
|---|---|
| **[Getting started](getting-started.md)** | Install it, start it with no configuration at all, register a stub, and see what a refusal looks like. |
| **[Migrating from WireMock](migrating-from-wiremock.md)** | You have a mappings directory and a suite. What moves, what changes shape, what to turn on. |

## Reference

- **[Programmatic administration](programmatic-administration.md)** — managing
  mocks from code: the OpenAPI contract at `api/openapi.yaml`, and the
  TypeScript SDK generated from it, with the two server properties its test
  helpers encode so a suite need not rediscover them.

- **[The admin UI](admin-ui.md)** — the web interface compiled into the binary.
  Open the admin port in a browser; it is at `/__admin/mockulus/ui/` and the
  root redirects there. Browse and edit stubs with the server's refusals landing
  on the field they name, read the request journal, and ask why a request did
  not match — the last of which works with the journal off.

| | |
|---|---|
| **[Compatibility matrix](compatibility.md)** | Every catalogued behavior: supported or not, and which test proves it. Generated from the behavior catalog and the corpus, so it cannot claim what the gate does not enforce. |
| **[Deviations](deviations.md)** | The places mockulus deliberately answers differently, grouped by what you would be doing when you hit one. |
| **[Configuration](configuration.md)** | Every key, where it can come from, and which ones you actually touch. |
| **[Operations](operations.md)** | Deployment shapes, Couchbase, probes, graceful drain, degraded modes, tracing, and the metrics worth alerting on. |

## Deeper

- **[SPEC.md](../SPEC.md)** — the authoritative technical specification: the
  compatibility contract, the matching engine, the data model, the performance
  SLOs and the E2E gate. The documents above are written from it, and the
  behavior catalog is parsed out of it, so it is the thing that is true when
  they disagree.
- **[ROADMAP.md](../ROADMAP.md)** — what was deliberately left out of v1, with
  design sketches, and the explicit non-goals. Every 422 for an unsupported
  feature points here.
- **[test/e2e/README.md](../test/e2e/README.md)** — how the regression gate
  works, and how the "100% of observable behavior" claim is made falsifiable
  rather than asserted.

## Two things worth knowing before you deploy

**The journal is off by default**, where WireMock has it on. Verification
endpoints answer an error until `journal_enabled: true`, so a suite that calls
`verify()` needs it turned on. It is off because it is the feature that folds a
single-node WireMock under load, and paying for it should be a choice.

**Several replicas need a shared store.** With the memory or file driver each
pod keeps its own stubs, so a stub registered through the Service would be
served by one pod and 404 on the others. The Helm chart refuses to render that
configuration rather than installing something that looks fine and answers
inconsistently.
