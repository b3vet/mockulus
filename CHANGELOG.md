# Changelog

All notable changes to this project are documented here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) form.

Versioning follows SPEC §22.5: `v0.<milestone>.x` through M0–M5, carrying **no**
compatibility promise, and `v1.0.0` at M6 exit. After 1.0, the behavior of the
WireMock-compatible surface changes only in majors, and a 422 becoming a
supported feature is a minor.

## [Unreleased]

### Added

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

### Changed

- SPEC §13's table now spells `couchbase.query_timeout` in full rather than in
  shorthand, and documents port `0` as binding an ephemeral port — both fell out
  of generating the table from the configuration struct.
