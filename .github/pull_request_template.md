<!-- SPDX-License-Identifier: Apache-2.0 -->

## What this changes

<!-- One or two sentences. What behavior is different afterwards? -->

## Why

<!-- What problem does it solve? Link the issue if there is one. -->

---

### Behavior changes

If this PR changes externally observable behavior, it must also update the
behavior catalog and the E2E corpus. CI enforces this — the completeness gates
fail when a spec row has no catalog entry, or a catalogued behavior at or below
the milestone cursor has no passing case.

- [ ] `SPEC.md` updated, if the contract changed
- [ ] `test/e2e/catalog/` entry added or re-synced
- [ ] `test/e2e/corpus/` case added, with an assertion that satisfies the
      behavior's evidence contract
- [ ] For a compatibility change: the case is tagged `wm: verified` and its
      expectations were recorded against the pinned WireMock

### Checks

- [ ] Commits are signed off (`git commit -s`) — DCO
- [ ] `make test` and `make e2e` pass locally
- [ ] No new dependency, or the new one is on the SPEC §18 allowlist and the
      license gate is green
