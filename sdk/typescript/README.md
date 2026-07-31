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
first, so which pairs work together is stated rather than inferred:

| SDK     | Server  | Notes                                                          |
| ------- | ------- | -------------------------------------------------------------- |
| `0.1.x` | `1.1.x` | The admin surface as of the release this SDK was built beside. |

A server older than the row it is paired with is not a supported combination:
the SDK's types come from that server's contract, and a call it can express is
one an older server may answer `404` code `1001` to. The reverse — a newer
server, an older SDK — is safe by the project's own compatibility promise, since
after `1.0` the WireMock-compatible surface changes only in majors and a `422`
becoming a supported feature is a minor.

The table grows a row per release. It is not generated, because there is nothing
to generate it from until there is more than one of either to compare.

## How this package is kept honest

Three mechanisms, none of which rely on anyone remembering:

- The request and response types are **generated** from `api/openapi.yaml` and
  committed. CI regenerates and diffs them, so they cannot drift from the
  contract.
- The contract itself is **cross-checked against the server's behavior catalog**
  in both directions, so it cannot drift from the surface the server's own test
  gate enforces.
- The integration suite drives a **real mockulus process** it starts itself. A
  client that type-checks against the contract can still send something the
  server refuses, and that is the only place it shows up.

## License

Apache-2.0, the same as the server. See `LICENSE` and `NOTICE` at the repository
root.
