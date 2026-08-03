# Programmatic administration

Managing mocks from code — a service that sets up its own fixtures, a test suite
that asserts on traffic, a deployment script that loads a corpus.

Everything here goes through the admin API, which is
[wire-compatible with WireMock's](migrating-from-wiremock.md). So an existing
WireMock client library works unchanged against a mockulus, and if you already
have one, keep it. What follows is for teams who would rather not hand-roll the
calls, and for TypeScript in particular.

## The contract

`api/openapi.yaml` at the repository root is an authored OpenAPI 3.1 description
of the whole admin surface. It is not a convenience file — it is cross-checked
against the behavior catalog in both directions by `make contract-lint`, which
runs in CI, so an endpoint the server implements cannot go missing from it and
an endpoint it invents cannot survive.

Generate a client for any language from it. The TypeScript one below is
generated from it too, which is the point: there is one description and
everything else is downstream of it.

## The TypeScript SDK

[`@mockulus/admin-sdk`](../sdk/typescript/README.md) — a typed client with no
runtime dependencies, built on the platform's own `fetch`.

```ts
import { MockulusClient, stubFor, get, urlPathEqualTo, aResponse } from '@mockulus/admin-sdk';

const mockulus = new MockulusClient({ baseUrl: 'http://mockulus:9090', token: process.env.ADMIN_TOKEN });

await mockulus.mappings.create(
  stubFor(get(urlPathEqualTo('/api/orders/1')).willReturn(aResponse().withStatus(200).withJsonBody({ id: 1 }))),
);
```

Three things about it are worth knowing before you reach for it.

**It types the supported subset and nothing more.** A stub the SDK can express
is a stub the server registers. `equalToXml` is not a function you can call,
because mockulus refuses it — so the 422 you would have got at registration is a
type error before the call is written. The refusals about *placement* are typed
too: there is no `schemaVersion` to attach to a matcher that has no schema, and
`applyTruncationLast` without `truncateExpected` does not compile.

**Errors carry every problem, not the first.** mockulus collects the whole list
before answering, which is what makes a rejected mapping one round trip instead
of one per mistake. The client preserves that:

```ts
import { isMockulusError } from '@mockulus/admin-sdk';

try {
  await mockulus.mappings.create(mapping);
} catch (err) {
  if (isMockulusError(err)) {
    console.error(err.pointers()); // ['/request/bodyPatterns/0/equalToXml', '/postServeActions']
  }
}
```

**The test helpers encode two properties of the server** that every suite would
otherwise rediscover:

```ts
import { verify, suite } from '@mockulus/admin-sdk';

// Polls, because the journal is eventually consistent. Asking once is flaky.
await verify(mockulus, { method: 'GET', url: '/api/orders/1' }, { times: 1 });

// Namespaces and cleans up after itself, because the deployment is shared.
const fixtures = suite(mockulus, { prefix: 'checkout' });
await fixtures.register(stubFor(get(urlPathEqualTo(fixtures.url('/orders'))).willReturn(aResponse())));
await fixtures.cleanup(); // removes exactly what it registered
```

## Two things to get right whatever client you use

**Turn the journal on if you verify.** It is off by default — recording every
request costs memory and I/O that a mock serving 50k RPS should not pay unasked
— so `requests`, `count`, `find` and the near-miss queries answer `500` with
code `1010` until `journal_enabled: true`. That is a configuration answer, and
the SDK says so rather than handing back a server error.

**Namespace; do not reset.** These deployments are shared, and
`POST /__admin/reset` destroys stubs belonging to whoever else is on the mock.
Give your fixtures a URL prefix, tag them with metadata, and remove them by that
tag — `POST /__admin/mappings/remove-by-metadata` is the call, and `suite()`
above is it wrapped up. A suite that resets is a suite that will eventually
delete a colleague's work in the middle of their run.

## The coupling rule

If you are changing mockulus rather than using it: a change to the admin surface
updates `api/openapi.yaml`, the generated types and the affected SDK code **in
the same pull request**, exactly as a behavior change updates the behavior
catalog and the E2E corpus. It is stated in [AGENTS.md](../AGENTS.md) and
enforced by `make contract-lint` plus the generated-types drift gate, so a route
that skips the contract fails CI rather than review.
