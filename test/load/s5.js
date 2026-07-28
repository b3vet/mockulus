// SPDX-License-Identifier: Apache-2.0
//
// S5 — SPEC §16.1's S1 shape with the request journal on: one stub, exact URL
// match, GET, a 256 B body, and every served request recorded. Target on the
// reference rig (1 pod, 2 vCPU, 512Mi): >= 25k RPS sustained, and a drop rate
// of zero at 10k RPS while Couchbase is healthy.
//
// The journal is start-time configuration and no stub can turn it on, so this
// measures a deployment started with `journal_enabled: true`
// (MOCKULUS_JOURNAL_ENABLED=true — the `journal` config variant of SPEC §19.4)
// and backed by the couchbase store, because the drop half is about what
// happens between the queue and the store. setup() refuses to run against a
// journal-disabled instance rather than spending two minutes recording S1's
// numbers a second time under S5's name.
//
// One scenario, two runs. The offered rate is the knob and each threshold is
// gated on the rate its criterion is stated at, so a run asserts the half it
// is actually measuring:
//
//   k6 run test/load/s5.js                       # offer 25k, the throughput half
//   k6 run -e RATE=10000 test/load/s5.js         # the drop-rate half, at 10k
//   k6 run -e BASE=http://host:8080 -e ADMIN=http://host:9090 test/load/s5.js
//
// ADMIN defaults to the admin listener rather than to BASE the way S1's does,
// because the counters this script reasons about are on /metrics and /metrics
// is only ever served on the admin port (SPEC §14.1).
//
// An operational note for whoever runs it: at 25k RPS a ninety-second run
// leaves a few million documents in the journal collection. They expire on
// `journal_ttl` (30 m), and teardown deliberately does not call
// DELETE /__admin/requests to hurry that along — clearing enumerates the
// collection, which is a far more violent thing to do to the cluster than
// waiting out the TTL.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || 'http://localhost:9090';
const RATE = Number(__ENV.RATE || 25000);
const RAMP = __ENV.RAMP || '30s';
const DURATION = __ENV.DURATION || '60s';
const SETTLE = Number(__ENV.SETTLE || 3);

// The stub is S1's, under S5's own prefix so the two cannot collide on a shared
// rig. Nothing about the stub changes here; what changes is the instance
// recording the requests it serves.
const STUB_PATH = '/load/s5/resource';
const BODY_256 = 'x'.repeat(256);

// The journal's own counters, sampled off /metrics either side of the run and
// reported as their growth over it. They are k6 metrics rather than a check so
// that the drop criterion can be a threshold: a run that dropped an entry has
// to fail the way a missed p99 fails, not print a line nobody reads.
const journalDropped = new Counter('journal_dropped');
const journalEnqueued = new Counter('journal_enqueued');
const storeErrors = new Counter('store_errors');

export const options = {
  scenarios: {
    s5_journal: {
      executor: 'ramping-arrival-rate',
      startRate: 5000,
      timeUnit: '1s',
      preAllocatedVUs: 200,
      maxVUs: 4000,
      stages: [
        { target: RATE, duration: RAMP },
        { target: RATE, duration: DURATION },
      ],
    },
  },
  thresholds: {
    'http_req_failed': ['rate==0'],

    // The throughput target, asserted as "every arrival the executor offered
    // was actually made". A rig that cannot absorb the offered rate leaves
    // iterations unstarted and k6 counts those in dropped_iterations, so zero
    // of them at an offered 25k is the target met. Like S1's p99 this is stated
    // at a measurement point rather than everywhere: a run driven past 25k is
    // looking for the ceiling and is allowed to find it, so the threshold
    // applies only at or below the target rate. It is a client-side signal — a
    // k6 host starved of CPU drops iterations too — which is exactly why §16.1
    // puts the load generators on their own node.
    ...(RATE <= 25000 ? { 'dropped_iterations': ['count==0'] } : {}),

    // The drop half. `mockulus_journal_dropped_total` counts both ends of the
    // journal's back pressure: an entry refused by a full queue, and a whole
    // batch lost to a flush that failed. Zero growth across the run is the
    // criterion and the criterion is stated at 10k, so a run offered more than
    // that records the number without gating on it.
    ...(RATE <= 10000 ? { 'journal_dropped': ['count==0'] } : {}),

    // A guard against passing for the wrong reason. Both counters above are
    // read in teardown, and a zero drop count means nothing unless something
    // proves the reading happened at all and that the instance was recording
    // while it ran. Requiring the enqueued counter to have moved fails the run
    // loudly in either case, instead of reporting an untroubled zero.
    'journal_enqueued': ['count>0'],

    // SPEC states no latency target for S5, so this is not a release criterion.
    // It is the line between absorbing the offered rate and queueing it, set an
    // order of magnitude above S1's 2 ms at 20k so that the journal's extra
    // work per request cannot trip it and only a collapse can.
    'http_req_duration': ['p(99)<25'],
  },
};

// readCounter sums one counter family out of the Prometheus text exposition.
// Summing rather than reading the first matching line is what keeps
// `mockulus_store_errors_total` honest — it is labelled by operation, and a
// reader that stopped at the first line would miss the operation that started
// failing.
function readCounter(body, name) {
  let total = 0;
  for (const line of body.split('\n')) {
    if (line === '' || line.charAt(0) === '#' || !line.startsWith(name)) {
      continue;
    }
    // The match has to end where the sample's name ends, or a metric that
    // merely begins with this name would be added in with it.
    const next = line.charAt(name.length);
    if (next !== ' ' && next !== '{') {
      continue;
    }
    const value = Number(line.slice(line.lastIndexOf(' ') + 1));
    if (!isNaN(value)) {
      total += value;
    }
  }
  return total;
}

// sample reads the three counters this scenario reasons about.
function sample() {
  const res = http.get(`${ADMIN}/metrics`);
  if (res.status !== 200) {
    throw new Error(
      `could not read ${ADMIN}/metrics: ${res.status} — S5's drop criterion is ` +
        `measured from those counters, so metrics_enabled must be on and ADMIN ` +
        `must point at the admin listener`
    );
  }
  return {
    dropped: readCounter(res.body, 'mockulus_journal_dropped_total'),
    enqueued: readCounter(res.body, 'mockulus_journal_enqueued_total'),
    storeErrors: readCounter(res.body, 'mockulus_store_errors_total'),
  };
}

// setup registers the stub through the public admin API — the harness uses no
// private hooks, exactly as the E2E gate does not (SPEC §19.1) — and first
// establishes that this deployment is the one S5 is about.
export function setup() {
  // A journal-disabled instance answers every journal-backed endpoint with the
  // disabled error (deviation #1), so one verification call settles the config
  // variant from outside, with no test hook and nothing to keep in step with
  // the product. It is asked before anything is registered so that a wrong
  // deployment costs a request rather than a run.
  const journal = http.get(`${ADMIN}/__admin/requests?limit=1`);
  if (journal.status !== 200) {
    throw new Error(
      `the request journal is off on this deployment: GET /__admin/requests ` +
        `answered ${journal.status} ${journal.body}. S5 is S1 with the journal ` +
        `enabled, so start mockulus with journal_enabled: true ` +
        `(MOCKULUS_JOURNAL_ENABLED=true) and run it again`
    );
  }

  const stub = {
    request: { method: 'GET', urlPath: STUB_PATH },
    response: {
      status: 200,
      body: BODY_256,
      headers: { 'Content-Type': 'text/plain' },
    },
  };

  const res = http.post(`${ADMIN}/__admin/mappings`, JSON.stringify(stub), {
    headers: { 'Content-Type': 'application/json' },
  });
  if (res.status !== 201) {
    throw new Error(`could not register the S5 stub: ${res.status} ${res.body}`);
  }

  const warm = http.get(`${BASE}${STUB_PATH}`);
  if (warm.status !== 200) {
    throw new Error(`S5 stub does not serve: ${warm.status} ${warm.body}`);
  }

  // Sampled last, immediately before the load starts, so the window the
  // counters describe is the run and not the preparation for it.
  return { id: JSON.parse(res.body).id, before: sample() };
}

export default function () {
  const res = http.get(`${BASE}${STUB_PATH}`);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'body is intact': (r) => r.body.length === 256,
  });
}

export function teardown(data) {
  if (!data) {
    return;
  }

  // The stub goes first and on its own: every other scenario shares this rig,
  // and a scrape that fails must not be the reason S5's stub outlives the run.
  if (data.id) {
    http.del(`${ADMIN}/__admin/mappings/${data.id}`);
  }

  // The writer batches, so the counter is still moving after the load stops:
  // up to `journal_flush_interval` (200 ms by default) passes between the last
  // request and the flush that would drop its batch. Reading immediately would
  // let a run that lost its final batches report zero drops, which is the one
  // answer this threshold must never give by accident.
  sleep(SETTLE);

  const after = sample();
  journalDropped.add(after.dropped - data.before.dropped);
  journalEnqueued.add(after.enqueued - data.before.enqueued);

  // Recorded, never gated. The SLO is a drop rate of zero *while Couchbase is
  // healthy*, so whoever reads a failed drop threshold needs to know whether
  // the store was up underneath it. The flusher holds the driver directly
  // rather than the instrumented wrapper, so its own failures are not in this
  // family; what is in it is the epoch poll, which touches Couchbase once per
  // `sync_interval` for the whole run and counts an error whenever it cannot.
  // Growth here means the run failed the SLO's precondition and should be
  // repeated against a healthy cluster, not that the target was missed.
  storeErrors.add(after.storeErrors - data.before.storeErrors);
}
