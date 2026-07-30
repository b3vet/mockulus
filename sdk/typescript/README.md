# @mockulus/admin-sdk

A typed client for the [mockulus](https://github.com/b3vet/mockulus) admin API —
for managing mocks programmatically from a service, a test suite, or a
deployment script.

> **Not published yet.** This package is being built alongside the server on the
> way to its first release. What is described below as _available today_ is what
> the package actually exports; everything else is stated as what it will be, so
> that this file is never ahead of the code.

## What it is for

mockulus refuses a stub it cannot serve, at registration, with a 422 naming the
field. That is the property that makes a migration tractable — you learn about
every gap in one command instead of at three in the morning. This SDK moves the
same contract one step earlier: it types the **supported subset and nothing
more**, so a stub you can express here is a stub the server registers, and a
field it does not support is a type error before it is an HTTP response.

That is also the reason the types are strict rather than permissive. An
`additionalProperties: false` in the contract is not pedantry; it is the 422 you
would otherwise get, delivered by the compiler.

## Available today

The error codes of the server's catalog, and the HTTP status each is answered
with:

```ts
import { ErrorCode, ErrorCodeStatus } from '@mockulus/admin-sdk';

ErrorCode.JournalDisabled; // 1010
ErrorCodeStatus[ErrorCode.JournalDisabled]; // 500
```

Two of the codes are WireMock's own — `10` and `109` — kept at its values so a
client that already special-cases one keeps working. Everything from `1000` up
is mockulus', where WireMock has no code and nothing can collide.

Branch on the **code**, not the status: five different problems answer `422`,
and the code is what tells them apart.

These constants are checked against the server's specification by this package's
own test suite, in both directions, so they cannot drift from what the server
actually answers.

## Coming with the releases that follow

- `MockulusClient` — the typed client, with namespaces mirroring the API and
  every non-2xx mapped to a `MockulusError` carrying the parsed `errors[]`.
- WireMock-style builders — `stubFor(get(urlPathEqualTo('/x')).willReturn(…))`,
  covering exactly the supported matcher and response set.
- Test helpers — `verify()` that understands the journal is eventually
  consistent, and a suite helper that namespaces and cleans up after itself
  instead of resetting a deployment other people are sharing.

## Requirements

Node 20 or newer. ESM only. **No runtime dependencies** — the client is built on
the platform's own `fetch`.

## Compatibility

The SDK versions independently of the server, because it will iterate faster at
first. A table stating which SDK versions work against which server versions
lands with the client, when there is more than one of either to state.

## License

Apache-2.0, the same as the server. See `LICENSE` and `NOTICE` at the repository
root.
