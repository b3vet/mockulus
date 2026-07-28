// SPDX-License-Identifier: Apache-2.0
//
// S6 — the cold-start scenario of SPEC §16.1: an instance that comes up with
// 10k stubs already in Couchbase must answer /readyz within 5 s.
//
// The clock for this SLO starts before any HTTP surface exists, and k6 cannot
// start the process it is measuring, so S6 is split rather than pretended.
// s6-driver.sh owns the container lifecycle and hands over the instant it
// launched the instance as T0_MS; this script owns everything reachable over
// HTTP — seeding the corpus, waiting on /readyz from that instant, proving the
// instance really came up with the corpus loaded, and failing the run when the
// budget is missed. The observer that saw the process start supplies t0, and
// the observer that can assert something about the result owns the threshold,
// which is also why the 5 s budget is a constant here and not an __ENV knob: a
// gate a CI job can widen from the command line is not a gate.
//
// Three phases, because the corpus has to outlive a single k6 process — the
// whole scenario is that the stubs are already in the store when the process
// starts, so the run that seeds them cannot be the run that removes them.
// KEEP=true is what defers the sweep to the caller; without it every phase
// cleans up after itself, which is what makes a bare run safe on a shared rig:
//
//   k6 run test/load/s6.js                          # seed, verify, sweep
//   k6 run -e PHASE=seed -e KEEP=true test/load/s6.js
//   k6 run -e PHASE=measure -e T0_MS=1753698000000 -e KEEP=true test/load/s6.js
//   k6 run -e PHASE=sweep test/load/s6.js
//
// The measure phase refuses to run without T0_MS. Timed from its own start it
// would be timing k6, and against an instance that was already up it would
// answer on the first poll and score a passing zero — the two ways this
// measurement can lie, so neither is left available.

import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Gauge, Trend } from 'k6/metrics';

// ADMIN points at the ops port rather than defaulting to BASE the way S1 does:
// /readyz lives only there (SPEC §15.2), and it is the surface being measured.
const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || 'http://localhost:9090';

const PHASE = __ENV.PHASE || 'seed';
const KEEP = (__ENV.KEEP || 'false') === 'true';
const STUBS = Number(__ENV.STUBS || 10000);
const BATCH = Number(__ENV.BATCH || 500);
const MIX = __ENV.MIX || 's2';
const STORE = __ENV.STORE || 'couchbase';
const POLL_MS = Number(__ENV.POLL_MS || 25);
const DEADLINE_MS = Number(__ENV.DEADLINE_MS || 30000);
const MAX_UPTIME_S = Number(__ENV.MAX_UPTIME_S || 60);

// The release criterion of SPEC §16.1, in seconds. Deliberately not settable.
const BUDGET_SECONDS = 5;

const PREFIX = '/load/s6';
const BODY_256 = 'x'.repeat(256);

// Every stub carries the same metadata object, which is what the sweep selects
// on. SPEC §5.1 asks CI runners sharing a deployment to clean up this way
// rather than by calling a global reset, and a shared load rig is exactly that
// situation: `POST /__admin/reset` here would take S1's stubs with it.
const SUITE = { suite: 'load-s6' };

const coldStart = new Trend('s6_cold_start_seconds');
const seedSeconds = new Trend('s6_seed_seconds');
const stubsLoaded = new Gauge('s6_snapshot_stubs');

export const options = {
  scenarios: {
    [`s6_${PHASE}`]: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: __ENV.MAX_DURATION || '10m',
    },
  },
  // Seeding and sweeping 10k stubs are 10k store round trips apiece, which is
  // minutes on a real Couchbase and far past k6's one-minute defaults.
  setupTimeout: __ENV.SETUP_TIMEOUT || '15m',
  teardownTimeout: __ENV.TEARDOWN_TIMEOUT || '15m',
  thresholds: thresholdsFor(PHASE),
};

// The sweep phase asserts nothing and so is given nothing to assert: a
// threshold over a metric this phase never writes to is a verdict on no
// evidence, which is not the sort of thing a cleanup step should be able to
// fail a job with. Its own failure path is the exception sweep() raises.
function thresholdsFor(phase) {
  if (phase === 'sweep') {
    return {};
  }
  if (phase !== 'measure') {
    return { checks: ['rate==1'] };
  }
  return {
    // http_req_failed is deliberately absent here: the poll loop starts while
    // the instance is still booting, so refused connections are the
    // measurement rather than a fault.
    checks: ['rate==1'],
    s6_cold_start_seconds: [`max<${BUDGET_SECONDS}`],
  };
}

export function setup() {
  if (PHASE === 'seed') {
    return seed();
  }
  if (PHASE === 'sweep') {
    return {};
  }
  if (PHASE !== 'measure') {
    throw new Error(`unknown PHASE ${PHASE}: expected seed, measure or sweep`);
  }
  if (!__ENV.T0_MS) {
    throw new Error(
      'the measure phase needs -e T0_MS=<epoch ms at process launch>; s6-driver.sh supplies it',
    );
  }
  return { t0: Number(__ENV.T0_MS) };
}

export default function (data) {
  if (PHASE === 'seed') {
    verifyCorpus(`${STUBS} stubs seeded`);
    return;
  }
  if (PHASE === 'sweep') {
    return;
  }

  const t0 = data.t0;
  const deadline = t0 + DEADLINE_MS;
  let ready = 0;
  while (Date.now() < deadline) {
    // A booting instance refuses the connection until the admin listener binds
    // and answers 503 until the snapshot is built, and k6 reports both as a
    // response rather than throwing, so one condition covers the whole edge.
    const res = http.get(`${ADMIN}/readyz`, { timeout: '2s', tags: { probe: 'readyz' } });
    if (res.status === 200) {
      ready = Date.now();
      break;
    }
    sleep(POLL_MS / 1000);
  }

  // The elapsed time is recorded even when the deadline expired first, so a
  // boot that never finished fails on the threshold with a number attached
  // instead of disappearing into a timeout with no measurement at all.
  const elapsed = ((ready || Date.now()) - t0) / 1000;
  coldStart.add(elapsed);
  console.log(
    `s6 cold start: ready in ${Math.round(elapsed * 1000)} ms of a ` +
      `${BUDGET_SECONDS * 1000} ms budget  s6_cold_start_ms=${Math.round(elapsed * 1000)}`,
  );
  if (!ready) {
    fail(`/readyz never answered 200 within ${DEADLINE_MS} ms of launch`);
  }

  verifyCorpus(`ready in ${elapsed.toFixed(3)} s`);
}

export function teardown() {
  // KEEP has nothing to say to the phase whose whole job is the removal.
  if (KEEP && PHASE !== 'sweep') {
    // The one path that leaves stubs behind, and it does so on purpose: the
    // measure phase needs the corpus in the store across a restart this
    // process cannot perform. s6-driver.sh sets KEEP on every phase it runs
    // and sweeps once from an exit trap, so the corpus outlives any single k6
    // process but never the run.
    console.log('s6 corpus left in place (KEEP=true); the caller owns the sweep');
    return;
  }
  sweep();
}

// seed imports the corpus in batches through the public admin API. One call
// carrying all 10k would fit under max_body_bytes, but it would also hold a
// single request open for every store write in the set; batching keeps each
// call inside a sane timeout and gives a failure a stub range to name.
function seed() {
  const started = Date.now();
  for (let from = 0; from < STUBS; from += BATCH) {
    const to = Math.min(from + BATCH, STUBS);
    const mappings = [];
    for (let i = from; i < to; i++) {
      mappings.push(stubFor(i));
    }

    const body = JSON.stringify({
      mappings: mappings,
      // OVERWRITE plus deterministic ids makes seeding idempotent: a rerun
      // after a crashed one replaces the corpus in place rather than doubling
      // it, and preserves each stub's seq while doing so.
      importOptions: { duplicatePolicy: 'OVERWRITE' },
    });

    const res = http.post(`${ADMIN}/__admin/mappings/import`, body, {
      headers: { 'Content-Type': 'application/json' },
      timeout: __ENV.IMPORT_TIMEOUT || '300s',
    });
    if (res.status !== 200) {
      throw new Error(`import of stubs ${from}..${to - 1}: ${res.status} ${res.body}`);
    }
  }

  const seconds = (Date.now() - started) / 1000;
  seedSeconds.add(seconds);

  // On a single-replica rig the admin write path has already rebuilt the
  // snapshot by the time the import answers; the short wait is for a rig with
  // more than one replica, where the others converge within sync_interval.
  const deadline = Date.now() + 10000;
  let state = health();
  while (Date.now() < deadline && (!state || state.stubs < STUBS)) {
    sleep(0.1);
    state = health();
  }
  if (!state) {
    throw new Error('the admin API did not answer /__admin/health after seeding');
  }
  if (state.stubs < STUBS) {
    throw new Error(`snapshot holds ${state.stubs} stubs after seeding ${STUBS}`);
  }

  console.log(
    `s6 seeded ${STUBS} stubs (mix=${MIX}) in ${seconds.toFixed(1)} s; ` +
      `store=${state.driver}, snapshot=${state.stubs}`,
  );
  return { seeded: STUBS };
}

// verifyCorpus is what stops a fast answer from being mistaken for a fast cold
// start. /readyz turning 200 over an empty snapshot, or over the instance that
// was supposed to have been restarted, would otherwise score a pass.
function verifyCorpus(context) {
  const state = health();
  if (!state) {
    fail('the admin API did not answer /__admin/health');
  }
  stubsLoaded.add(state.stubs);

  check(state, {
    'snapshot carries the seeded corpus': (s) => s.stubs >= STUBS,
  });
  if (PHASE === 'measure') {
    check(state, {
      // Seeding works against any store, so only the phase that depends on one
      // asserts it: a cold start over the memory driver had nothing to load,
      // and is a boot the SLO says nothing about.
      'store is the one the SLO is stated against': (s) => s.driver === STORE,
      // A restart the driver failed to perform leaves an instance that has
      // been up for minutes, and it would answer the first poll instantly.
      'the instance under measurement is a fresh one': (s) => s.uptime <= MAX_UPTIME_S,
    });
  }

  // Serving is the property, not the stub count: a snapshot that loaded but
  // does not answer is not ready in any sense the SLO cares about.
  for (const i of probeIndexes()) {
    const res = http.get(`${BASE}${PREFIX}/exact/${pad5(i)}`);
    check(res, {
      'seeded stub serves 200': (r) => r.status === 200,
      'seeded body is intact': (r) => r.body.length === 256,
    });
  }

  console.log(
    `s6 verified: ${context}; snapshot=${state.stubs} stubs, store=${state.driver}, ` +
      `uptime=${state.uptime}s`,
  );
}

// sweep removes the corpus by metadata rather than by id. Ten thousand deletes
// driven from k6 would be ten thousand requests and would leave the corpus
// half-removed if the run were interrupted partway; one call is atomic enough
// to be safe to repeat, and removing nothing answers 200 with an empty list.
function sweep() {
  const res = http.post(
    `${ADMIN}/__admin/mappings/remove-by-metadata`,
    JSON.stringify({ equalToJson: JSON.stringify(SUITE) }),
    {
      headers: { 'Content-Type': 'application/json' },
      timeout: __ENV.SWEEP_TIMEOUT || '600s',
    },
  );
  if (res.status !== 200) {
    // Leaving 10k stubs on a rig every other scenario shares is a failure of
    // this run, not a footnote to it.
    fail(`could not sweep the S6 corpus: ${res.status} ${res.body}`);
  }
  console.log(`s6 swept ${JSON.parse(res.body).meta.total} stubs`);
}

// health reads the admin health document, which reports the store driver in
// use, the snapshot's stub count and the process uptime — the three facts this
// harness needs about an instance, all on a public surface.
function health() {
  const res = http.get(`${ADMIN}/__admin/health`, { timeout: '10s' });
  if (res.status !== 200) {
    return null;
  }
  const doc = JSON.parse(res.body);
  return {
    stubs: doc.stubs,
    uptime: doc.uptimeInSeconds,
    driver: doc.store.driver,
  };
}

// stubFor builds stub i. The default mix is S2's documented shape (SPEC §16.1)
// scaled to 10k, because cold start is dominated by compiling matchers and a
// corpus of nothing but exact URLs would measure a boot no deployment performs.
// MIX=exact reproduces the shape BASELINE.md's Rebuild/cold row was measured
// against, for comparing the two numbers directly.
function stubFor(i) {
  const stub = {
    id: uuidFor(i),
    metadata: SUITE,
    // Persistent stubs carry no TTL. The corpus has to survive a restart and a
    // queued CI job, and ephemeral_stub_ttl expiring it mid-run would read as
    // a cold start that lost stubs. The sweep is what removes them.
    persistent: true,
    response: {
      status: 200,
      body: BODY_256,
      headers: { 'Content-Type': 'text/plain' },
    },
  };

  const kind = MIX === 'exact' ? 0 : i % 10;
  if (kind < 7) {
    stub.request = { method: 'GET', urlPath: `${PREFIX}/exact/${pad5(i)}` };
  } else if (kind < 9) {
    stub.request = {
      method: 'GET',
      urlPathPattern: `${PREFIX}/re/${pad5(i)}/[a-f0-9]{8}`,
    };
  } else {
    stub.request = {
      method: 'POST',
      urlPath: `${PREFIX}/jp/${pad5(i)}`,
      // The expression differs per stub on purpose. A corpus of a thousand
      // identical paths would compile once behind any expression cache and
      // report a cold start that a real stub set never gets.
      bodyPatterns: [{ matchesJsonPath: `$.order.item${pad5(i)}` }],
    };
  }
  return stub;
}

// probeIndexes picks stubs from the ends and the middle of the corpus. They are
// all multiples of ten, which under either mix is an exact-URL stub, so one
// request shape probes both.
function probeIndexes() {
  if (STUBS <= 10) {
    return [0];
  }
  const middle = Math.floor(STUBS / 20) * 10;
  const last = Math.floor((STUBS - 1) / 10) * 10;
  return [0, middle, last].filter((v, idx, all) => all.indexOf(v) === idx);
}

// Ids are derived from the index rather than left to the server so a rerun
// overwrites the corpus instead of stacking a second one beside it. Mockulus
// requires a canonical UUID (SPEC §5.2), so the index goes in the node field.
function uuidFor(i) {
  return `f6000000-0000-4000-8000-${hex12(i)}`;
}

function hex12(n) {
  return `000000000000${n.toString(16)}`.slice(-12);
}

function pad5(n) {
  return `00000${n}`.slice(-5);
}
