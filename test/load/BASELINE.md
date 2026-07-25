# Performance baselines

The recorded numbers every later milestone is compared against. SPEC §16.2 fails
a run that regresses more than 10% on RPS or p99 against the stored baseline for
the same rig.

A baseline is only comparable within its own rig. Record the rig with the
number, always.

## S1 — one stub, exact URL match, GET

**Target (SPEC §16.1):** ≥ 50k RPS sustained, p99 < 2 ms at 20k RPS, 256 B body.

### dev-arm64 (M0, 2026-07-25)

Not the reference rig: k6 runs on the same machine as mockulus and competes for
the remaining cores, so these numbers are a floor rather than a ceiling. They
exist to catch regressions early; the release gate is the reference rig below.

| | |
|---|---|
| Host | Apple silicon, 10 cores, darwin/arm64 |
| mockulus | `GOMAXPROCS=2`, memory store, journal off, log level warn |
| Load | k6 v1.2.3, same host, keep-alive on |
| Build | `-trimpath`, version `m0-baseline` |

| Measurement | Result | Target | |
|---|---|---|---|
| Sustained throughput (40 s @ 50k arrival rate) | **49,620 RPS**, 0 failed of 1,984,912 | ≥ 50k | met |
| p99 @ 20k RPS (25 s) | **1.07 ms** | < 2 ms | met |
| p95 @ 20k RPS | 0.117 ms | — | |
| median @ 20k RPS | 0.032 ms | — | |
| Errors | 0.00% across every run | 0 | met |

Reproduce:

```sh
GOMAXPROCS=2 MOCKULUS_LOG_LEVEL=warn ./bin/mockulus &
k6 run -e MODE=latency -e RATE=20000 -e DURATION=25s test/load/s1.js
k6 run -e MODE=latency -e RATE=50000 -e DURATION=40s test/load/s1.js
```

### reference-rig

Not yet recorded. The reference rig of SPEC §16.1 is 1 pod with
`limits: {cpu: 2, memory: 512Mi}` on kind or comparable, with k6 clients on a
separate machine. The nightly perf job records it, and the release gate reads
from it.

## S2–S10

Not yet recorded — they need the stub sets, templating, scenarios and journal of
M1 through M5. Each lands with its milestone.
