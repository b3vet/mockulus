# CLAUDE.md

The instructions for working in this repository are in **[AGENTS.md](AGENTS.md)**. Read it
before changing anything: it carries the repo orientation, the rule that a behavior change
updates the behavior catalog and the E2E corpus in the same PR, the sibling rule that an
admin-surface change updates the OpenAPI contract and the SDK in the same PR, how to run
the gates, and the discipline for probing an external reference.

[CONTRIBUTING.md](CONTRIBUTING.md) is the contract a pull request is held to — sign-off,
what CI checks, and how compatibility bugs are filed.

This file exists because some tooling looks for this name and not the other one, and it
deliberately carries no rules of its own. Two copies of a rule become one true copy and one
stale copy, and there is no way to tell from either which is which. Anything that would be
added here belongs in `AGENTS.md`.
