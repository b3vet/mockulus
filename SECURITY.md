# Security Policy

## Supported versions

Security fixes land on the latest minor release. Older minors are not patched.

| Version | Supported |
|---|---|
| Latest minor | Yes |
| Anything older | No — upgrade |

Pre-1.0 releases (`v0.x`) carry no compatibility or support promise.

## Reporting a vulnerability

**Do not open a public issue.** Report privately through GitHub Security
Advisories ("Report a vulnerability" on the Security tab of this repository).

Please include the affected version, a description of the impact, and enough
detail to reproduce. We will acknowledge receipt, keep you updated on our
assessment, and credit you in the advisory unless you prefer otherwise.

## Threat model

Mockulus is a test-infrastructure component. Two boundaries matter:

**Mock traffic is untrusted input.** Request bodies and headers are capped,
regex evaluation is bounded by a match timeout so a pathological pattern cannot
hang a worker, and error responses on the mock port never reflect internals.

**The admin API mutates what every replica serves.** It is unauthenticated by
default because the expected posture is in-cluster only, behind a NetworkPolicy,
with `admin_on_mock_port: false` when the mock port is exposed beyond the
namespace. Set `admin_auth_token` when that posture does not hold. The Helm
chart ships a hardened values preset.

Templates are sandboxed by construction: only allowlisted helpers are
registered, and helpers that would reach the filesystem, environment, network or
host (`file`, `systemValue`, `secret`, `hostname`) are deliberately excluded.

## Handling stub content

Stub bodies and headers may contain whatever a team put in their mocks,
including credentials for other systems. Mockulus never logs bodies or headers
at info level, and the journal has a bounded TTL. **Treat the backing Couchbase
bucket as sensitive data.**
