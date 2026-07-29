# Changelog

All notable changes to this project are documented here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) form.

Versioning follows SPEC §22.5: `v0.<milestone>.x` through M0–M5, carrying **no**
compatibility promise, and `v1.0.0` at M6 exit. After 1.0, the behavior of the
WireMock-compatible surface changes only in majors, and a 422 becoming a
supported feature is a minor.

## [Unreleased]

## [1.0.0] - 2026-07-29

The first release. The compatibility surface is complete, the engine is
finished, and every question the differential harness raised has been answered —
fixed where mockulus was wrong, numbered in SPEC §5.5 where the difference is
deliberate.

One M6 exit criterion is **deliberately deferred rather than met**: §20 asks for
all ten performance scenarios green on the reference rig of §16.1, and that rig —
one pod at 2 vCPU with the load generator on a separate machine — does not exist
yet. The scenarios themselves do, each encoding its SLO as a threshold that
fails the run rather than printing a number for someone to interpret, and the S1
baseline has been reproduced on a developer machine. What is missing is the
hardware to state them against, not the means to measure. This is recorded here
rather than quietly dropped, because a release that claims an exit criterion it
skipped is worse than one that says which.

### Added

- The matching engine of SPEC §6: the immutable snapshot and its RCU swap, the
  candidate indexes, lazy request parsing, the cheap-to-expensive matcher order,
  the RE2/regexp2 seam with Java syntax translation, an in-repo JSONPath
  evaluator, near-miss scoring, and per-element compile quarantine.
- The full stub format of §5.2 and the admin API of §5.1 — mappings CRUD,
  bulk import with its duplicate policies, the files API, settings, reset — with
  the error catalog of Appendix B behind it. Every unsupported field is refused
  at registration with a pointer to the field and a link to the roadmap, and one
  response lists every problem it found rather than the first.
- The Couchbase store of §7: envelope documents, counters, TTLs, the epoch
  poller and resync sweep of §8, the write-path splice and compile cache of
  §4.3/§6.2, and the degraded modes of §4.6.
- Response templating (§10), scenarios with CAS transitions (§9), and the
  request journal with its verification endpoints (§11).
- The `file` store driver, which serves an existing WireMock project directory
  read-only so a team can point mockulus at the tree they already have.
- Documentation under `docs/`: getting started, a WireMock migration guide, a
  configuration reference, an operations guide, the deviation list in usable
  form, and a compatibility matrix generated from the behavior catalog and the
  corpus so it cannot claim support the gate does not enforce.
- The load scenarios S2–S10 of §16.1, each encoding its SLO as a threshold so a
  run that misses the target fails rather than reporting a number to be judged.

### Changed

- SPEC §5.5 grew from 3 deviations to 48. The additions are not new behavior:
  they are behavior the differential corpus found the compatibility claim was
  relying on without recording. Each carries a case pinning it.
- §6.6 now states that Java regex syntax is translated wherever the translation
  is exact, narrowing the RE2-versus-Java carve-out to the residue where no
  exact translation exists.
- The Go module path is settled at `github.com/b3vet/mockulus`, and §15.1's
  image target moves from an estimated 25 MB to a measured 40 MB.
- Appendix C is empty. Every item it tracked has been probed and folded into the
  section that owns it.

### Fixed

Twenty-nine behaviors where mockulus disagreed with pinned WireMock 3.13.2, all
found by the differential corpus rather than by report. The ones a user would
have noticed:

- Templated output was HTML-escaped, which corrupted any templated JSON
  containing `&`, `<`, `>`, `"` or `'`.
- `{{now}}` and `{{randomValue}}` rendered nothing, because a helper called with
  no arguments was parsed as a variable.
- A stub declaring a `Content-Length` smaller than its body served the header
  and then no bytes, so clients blocked or reported a truncated read.
- `equalToJson` compared numbers at float64 precision and therefore matched
  documents it should not have — a stub keyed on a wide numeric id also answered
  for its neighbour.
- A semicolon in a query string dropped the whole parameter, which also made
  `{"absent": true}` match a request that carried it.
- `$.xs.length()` compiled to a lookup for a member named `length()` and
  silently never matched, which §6.7 explicitly forbids.
- A null matcher operand registered and became the empty string, so
  `{"contains": null}` matched every request with a body.
- An upper-case stub id in an admin path did not resolve, though §5.2 says ids
  are parsed case-insensitively.
- A read-only store reported admin writes as "the stub store is unavailable",
  which reads as a Couchbase outage to someone running the file driver.
- A scenario's retry logic classified store errors by matching their text, so a
  scenario named after that text turned any outage into a silent retry loop.
- `binaryEqualTo` with an empty operand could not match an empty body, which
  made it the one body matcher unable to say "the body is empty".
- A mapping id in an admin path that was not a UUID answered a bare 404, which
  reads as "no such stub" for a segment that could never have named one.
- `request.id` echoed an inbound `X-Request-Id`, letting a caller choose what a
  template rendered — and two callers choose the same value.
- A quarantined stub logged how many problems its mapping had and not which
  fields, which for the file driver is the only report there is.

### Testing

- The E2E corpus grew to 537 cases over 213 catalogued behaviors, 353 of them
  replayed against pinned WireMock 3.13.2 on every merge and diffed step by
  step.
- Unit coverage of the §19.2 correctness core — match, matchers, stub, template,
  scenario — meets its 85% bar; three of the five are at 100%.
- The allocation ceilings of §16.3 are asserted outside the race suite, because
  the race detector allocates on its own account and not repeatably.

### Laid down in M0, and still standing

Everything above was built on this, and none of it changed shape on the way to
1.0.0 — which is the argument for having spent M0 on it.

- Repository, build tooling and the open-source governance of SPEC §22:
  Apache-2.0 throughout with an enforced SPDX header check, DCO sign-off,
  CONTRIBUTING encoding the working contract, SECURITY with the threat model,
  Contributor Covenant 2.1, CODEOWNERS, and a license gate that denies copyleft
  in the shipped binary's module graph.
- The typed configuration surface of SPEC §13: every key, bound from environment
  variables and an optional YAML file, with env > file > default precedence,
  exhaustive validation, secret redaction and `_FILE` mounts. The reference
  table in the spec is generated from the struct, and CI fails on drift.
- The serving skeleton of SPEC §4: two listeners with the routing of §12.1, the
  immutable snapshot and RCU swap of §6.2, the store interfaces of §7.1 with an
  in-memory driver, the error catalog of Appendix B, and the startup and drain
  sequences of §4.4 and §4.5. Matching covers method with exact `url` and
  `urlPath`; the rest of the matcher set arrives in M1 and is rejected with a
  422 until then rather than silently ignored.
- The E2E regression gate of SPEC §19: a black-box runner, a behavior catalog
  derived from the spec's own structured blocks with hash-pinned rows and
  machine-checkable evidence contracts, blocking completeness gates, and the
  first corpus. 181 behaviors catalogued.
- The k6 load harness and the S1 baseline of SPEC §16, recorded from the first
  milestone so "fast" stays falsifiable.
- Helm chart and kustomize manifests with probes, PDB, HPA, NetworkPolicy,
  topology spread and a hardened values preset.

- SPEC §13's table began spelling `couchbase.query_timeout` in full rather than
  in shorthand, and documenting port `0` as binding an ephemeral port — both
  fell out of generating the table from the configuration struct rather than
  maintaining it by hand.
