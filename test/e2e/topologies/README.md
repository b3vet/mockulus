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

**T2 and T3 are implemented.** Both run against one Couchbase container, pinned
by `COUCHBASE_VERSION` in this directory and started by the first case that
needs one — a run with no `couchbase` case in it never touches Docker.

T4 is driven by `scripts/kind-smoke.sh` and `scripts/kind-rollout.sh` from the
merge pipeline. T5 lands with M1, when the compatibility surface is large enough
for a differential run to say anything.

## The Couchbase lane

**One container per run**, shared by T2, T3 and every config variant. A
container per variant would put a minute and a gigabyte between a developer and
each `make e2e`, so deployments are separated by *scope* instead: T2's `default`
variant owns `t2-default`, T3's owns `t3-default`, and a scope is a distinct
keyspace, so one variant's TTL sweep or global reset cannot reach another's
stubs (SPEC §7.2).

The harness creates the bucket and nothing else. `couchbase.manage_bucket`
defaults to true, so mockulus creates its own scope, collections and journal
index at boot — the zero-config path real deployments use is the path the suite
exercises. Only the bucket needs cluster-manager rights the product deliberately
does not ask for. T3's three replicas are booted one at a time so they do not
race on that first bootstrap.

Bring-up waits on observable state, never on a duration: the management service
answering, `cluster-init` succeeding, the bucket's nodes reporting healthy, and
the query service listing the bucket. A sleep long enough for a loaded CI runner
would be most of a minute of dead time locally and would still be the suite's
likeliest flake (SPEC §19.1).

**The lane is exclusive to one run.** A Couchbase client is told where the
services live *by the cluster*, so the ports cannot be remapped and only one
container can exist per machine. A second run waits for the lane rather than
taking it — removing a container another run is using would turn one person's
`make e2e` into somebody else's mystery failure. A container whose run is gone
is nobody's, and gets cleared, so a run killed hard enough to skip its own
teardown self-heals on the next one.

**No Docker, no lane.** T2/T3 cases are then reported as skipped with the reason
attached. That is not a pass: skipped cases cannot satisfy the coverage gate, so
every behavior they would have covered is still reported uncovered and the run
is still red — the reason simply travels with the hole.

## The T3 load balancer

T3 puts a small round-robin reverse proxy in front of three replicas, standing
in for a ClusterIP Service. That is deliberate: it makes "any replica serves any
request and any admin call" something the suite proves continuously rather than
something the architecture merely claims. The mock port and the admin port each
get one, and balancing is per request, not per connection — a proxy that pinned
a client to a replica would let a broken replica through for as long as no case
happened to land on it.

Cases pin a specific replica with `pod: 0` when they need to observe
propagation, and use `pod: any` — the default — otherwise:

```yaml
- admin:   {pod: 0, method: POST, path: /__admin/mappings, body_file: stubs/order.json}
  expect:  {status: 201}
- request: {pod: 2, method: GET, path: /e2e/my-case/order}
  expect_eventually: {status: 200, within: 10s}
```

Both listeners of one pin belong to the same process, so a case can write
through pod 1's admin API and read pod 1's mock port. `metricsprobe` takes the
same `pod:` field, because counters are per process and an unpinned probe would
read whichever replica the round-robin chose. `logprobe` is the exception: it
searches every replica's output, so it asserts that *some* pod logged the line —
prefixing lines with their pod would stop them being the JSON a probe matches
fields in.

A case that pins past the first replica without declaring
`requires: [multi-pod]` is rejected at load, rather than failing three steps in
with a message about a replica that does not exist.

## T5 scope

Only single-pod shapes take part in differential runs. A distributed behavior
has no single-node WireMock to diff against, so those cases are `wm: n/a` and
their expectations come from the spec instead.
