# Topologies

The running shapes the corpus executes against (SPEC §19.4). Cases declare
`requires:` capabilities, and the runner schedules each onto the cheapest
topology that satisfies it.

| ID | Shape | Capabilities it provides | Where it runs |
|---|---|---|---|
| T1 | 1× mockulus, memory store | — | every PR, fastest lane |
| T2 | 1× mockulus + Couchbase | `couchbase` | every PR |
| T3 | 3× mockulus + Couchbase + round-robin proxy | `couchbase`, `multi-pod` | every PR |
| T4 | kind + the Helm chart | `kubernetes` | merge and release |
| T5 | mockulus + pinned WireMock | differential oracle | merge, nightly, release |

## Status

**T1 is implemented.** The runner boots mockulus directly on ephemeral ports and
reads the startup summary line to learn which ones it got. No containers, so a
full T1 lane costs about as much as starting a process per config variant.

T2 and T3 land with M2, when there is a Couchbase driver for them to exercise.
T4 is driven by `scripts/kind-smoke.sh` and `scripts/kind-rollout.sh` from the
merge pipeline. T5 lands with M1, when the compatibility surface is large enough
for a differential run to say anything.

## The T3 load balancer

T3 puts a small round-robin reverse proxy in front of three replicas, standing
in for a ClusterIP Service. That is deliberate: it makes "any replica serves any
request and any admin call" something the suite proves continuously rather than
something the architecture merely claims. Cases pin a specific replica with
`pod: 0` when they need to observe propagation, and use `pod: any` otherwise.

## T5 scope

Only single-pod shapes take part in differential runs. A distributed behavior
has no single-node WireMock to diff against, so those cases are `wm: n/a` and
their expectations come from the spec instead.
