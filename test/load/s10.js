// SPDX-License-Identifier: Apache-2.0
//
// S10 — the CI-storm scenario of SPEC §16.1: 100 stub creates a second spread
// across two pods while the S1 load runs. The targets on the reference rig are
// p99 create < 150 ms against Couchbase with durability none, and S1's own
// numbers still met while the storm is going on.
//
// The two halves are one script because the claim is about their overlap. Two
// separate runs, one storming and one loading, would report the same two
// numbers without ever having put them on the same clock, and what is worth
// gating only exists while they coincide: an admin write bumps the epoch (SPEC
// §4.3), every peer notices within sync_interval and rebuilds its whole
// snapshot level-triggered (§8), and that rebuild is CPU spent on a pod that is
// also serving mock traffic. Coalescing is what is supposed to make this
// survivable — a pod reloads about once per sync_interval however hard the
// cluster is being written to (§7.4) — and this scenario is where a claim like
// that either holds at 100 writes a second or stops being true.
//
// The storm creates and never deletes, so the stub set grows by STORM_RATE
// every second the run lasts and each rebuild is over a larger set than the one
// before it. That is deliberate rather than an oversight: it is what a CI fleet
// registering stubs for its tests does to a shared deployment, and a longer run
// is a harsher version of this test rather than a different one.
//
// The rig is not the single pod of compose.yaml. It is two mockulus pods
// sharing one Couchbase bucket and scope, left at `couchbase.durability: none`
// — the SLO is stated at that setting, and a deployment on `majority` will miss
// the create budget for a reason that is not a regression, so record the
// setting with the number. The mock load points at pod A rather than at a
// Service in front of both, so the pod whose p99 is being gated is also the pod
// taking half the writes, and so the number stays comparable with the
// single-pod S1 baseline it has to be read against.
//
//   k6 run -e POD_A=http://a:8080 -e ADMIN_A=http://a:9090 \
//          -e POD_B=http://b:8080 -e ADMIN_B=http://b:9090 test/load/s10.js
//   k6 run ... -e MODE=throughput test/load/s10.js  # S1's other target, same storm
//   k6 run ... -e STORM_RATE=250 test/load/s10.js   # past the SLO point

import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Trend, Counter } from 'k6/metrics';

const POD_A = (__ENV.POD_A || 'http://localhost:8080').replace(/\/+$/, '');
const POD_B = (__ENV.POD_B || 'http://localhost:8081').replace(/\/+$/, '');
const ADMIN_A = (__ENV.ADMIN_A || 'http://localhost:9090').replace(/\/+$/, '');
const ADMIN_B = (__ENV.ADMIN_B || 'http://localhost:9091').replace(/\/+$/, '');

// The address the mock stream drives, pod A unless told otherwise. A Service in
// front of both pods is a legitimate thing to point this at, but the p99 that
// comes back is then an aggregate over two pods rather than the per-pod number
// BASELINE.md stores for S1, and the comparison it exists for stops working.
const BASE = (__ENV.BASE || POD_A).replace(/\/+$/, '');

// S1 states two targets and no single run measures both, so this script carries
// the same two modes s1.js does and drives each of them under the same storm.
// Latency is the default because the headline number of §16.1 S10 — a create
// p99 next to a serve p99 — lives at the fixed measurement point.
const MODE = __ENV.MODE || 'latency';
const MOCK_RATE = Number(__ENV.RATE || 20000);

// The storm itself. 100 a second across the two pods is the SLO; anything above
// it is a knee-finding run and lifts the create threshold below.
const STORM_RATE = Number(__ENV.STORM_RATE || 100);

// Every stub this script registers carries this in its metadata so teardown can
// remove exactly what the run created. The global resets are not an option on a
// deployment shared with the other scenarios (SPEC §1).
const SUITE = __ENV.SUITE || 'load-s10';

const NS = '/load/s10/';
const BODY_256 = 'x'.repeat(256);
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

// A 404 is the expected answer while setup waits for a stub to reach pod B, and
// again when /metrics is asked for on an address that turns out to be a mock
// port. Neither is a failure of the deployment and neither belongs in the
// run's error rate.
const TOLERATED = http.expectedStatuses(200, 404);

// k6 takes durations as strings, and this script has to do arithmetic on one:
// the storm has to already be running when the mock stream starts measuring and
// still be running when it stops, or part of the p99 being gated was collected
// on a quiet rig. The knob keeps k6's spelling so it reads the same as it does
// in the other scripts, and is parsed here rather than passed through.
function seconds(value) {
  const parts = /^(\d+(?:\.\d+)?)(ms|s|m|h)?$/.exec(String(value).trim());
  if (!parts) {
    throw new Error(`could not read "${value}" as a duration — use a plain number of seconds or one of 500ms, 90s, 5m, 1h`);
  }
  const scale = { ms: 0.001, s: 1, m: 60, h: 3600 };
  return Number(parts[1]) * scale[parts[2] || 's'];
}

// Throughput mode reuses S1's ramp exactly, because the point of running it
// here is to compare the result with the S1 baseline and a different ramp would
// be comparing two shapes. It follows that the ramp is fixed and DURATION only
// means anything in latency mode.
const RAMP = [
  { target: 20000, duration: '30s' },
  { target: 50000, duration: '60s' },
  { target: 50000, duration: '60s' },
];

const MOCK_SECONDS = MODE === 'throughput'
  ? RAMP.reduce((total, stage) => total + seconds(stage.duration), 0)
  : seconds(__ENV.DURATION || '60s');

// What the storm gets to itself either side of the measurement window.
const LEAD = seconds(__ENV.LEAD || '10s');
const STORM_SECONDS = MOCK_SECONDS + 2 * LEAD;

// The storm's own delivery, counted rather than assumed. An arrival-rate
// executor that cannot keep up sheds iterations, and a run that quietly stormed
// at 60 a second has not measured the create latency of a 100-a-second storm
// however good the percentile looks. One per cent covers scheduling jitter at
// the edges of the window and nothing else.
const EXPECTED_CREATES = Math.floor(STORM_RATE * STORM_SECONDS * 0.99);

const createDuration = new Trend('s10_create_duration', true);
const creates = new Counter('s10_creates');

const mockScenario = MODE === 'throughput'
  ? {
      executor: 'ramping-arrival-rate',
      startRate: 5000,
      timeUnit: '1s',
      stages: RAMP,
      preAllocatedVUs: Number(__ENV.VUS || 200),
      maxVUs: Number(__ENV.MAX_VUS || 4000),
      startTime: `${LEAD}s`,
      exec: 'mockLoad',
    }
  : {
      executor: 'constant-arrival-rate',
      rate: MOCK_RATE,
      timeUnit: '1s',
      duration: `${MOCK_SECONDS}s`,
      preAllocatedVUs: Number(__ENV.VUS || 200),
      maxVUs: Number(__ENV.MAX_VUS || 2000),
      startTime: `${LEAD}s`,
      exec: 'mockLoad',
    };

// Both halves are thresholds, because both halves are the release criterion:
// "S1 targets still met during the storm" is an assertion that fails a run, not
// a number to read off a summary afterwards. The mock ones are tagged, which
// also keeps setup's own requests — the registration, the convergence poll, the
// metrics scrapes — out of the streams being gated.
const mockThresholds = MODE === 'throughput'
  ? {
      'http_req_failed{op:mock}': ['rate==0'],
      'http_req_duration{op:mock}': ['p(99)<10'],
    }
  : {
      'http_req_failed{op:mock}': ['rate==0'],
      // At a fixed arrival rate the rate is the measurement point, so a run
      // that could not deliver it has not measured the SLO. The counter is not
      // per scenario, which is the right shape here: a storm shedding
      // iterations invalidates the same run for the same reason. Throughput
      // mode gets no equivalent gate, because a ramp looking for the ceiling
      // drops iterations by definition once it finds it — s1.js reads that
      // target off the achieved rate against the baseline, and so does this.
      dropped_iterations: ['count==0'],
      ...(MOCK_RATE <= 20000 ? { 'http_req_duration{op:mock}': ['p(99)<2'] } : {}),
    };

export const options = {
  scenarios: {
    // The storm brackets the measurement window on both sides. Starting them
    // together would leave the first seconds of the mock stream measuring a rig
    // whose peers have not yet noticed the first write, and ending them
    // together would end the storm while the last requests are still being
    // timed — both of which flatter the p99 in the direction of passing.
    s10_admin_storm: {
      executor: 'constant-arrival-rate',
      rate: STORM_RATE,
      timeUnit: '1s',
      duration: `${STORM_SECONDS}s`,
      preAllocatedVUs: Number(__ENV.STORM_VUS || 50),
      maxVUs: Number(__ENV.STORM_MAX_VUS || 400),
      startTime: '0s',
      exec: 'adminStorm',
    },
    s10_mock_load: mockScenario,
  },
  setupTimeout: __ENV.SETUP_TIMEOUT || '5m',
  // Teardown removes everything the storm created, which at the SLO rate is
  // thousands of documents and one store round trip each.
  teardownTimeout: __ENV.TEARDOWN_TIMEOUT || '15m',
  thresholds: {
    checks: ['rate==1'],
    'http_req_failed{op:create}': ['rate==0'],
    's10_creates': [`count>=${EXPECTED_CREATES}`],
    // The 150 ms of §16.1 is stated for a 100-a-second storm, in the same way
    // S1's p99 is stated at 20k RPS, so the threshold applies at that rate and
    // a run pushed past it records the curve without gating on it.
    ...(STORM_RATE <= 100 ? { 's10_create_duration': ['p(99)<150'] } : {}),
    ...mockThresholds,
  },
};

// One stub shape for both halves. The storm writes the same document the mock
// stream is being served from, 256 B body and all, because what a create costs
// is largely what the document costs, and storming with something smaller than
// the rig serves would report a create latency no CI run actually pays.
function stubFor(path) {
  return {
    metadata: { suite: SUITE },
    request: { method: 'GET', urlPath: path },
    response: {
      status: 200,
      body: BODY_256,
      headers: { 'Content-Type': 'text/plain' },
    },
  };
}

function requireReady(name, admin) {
  const res = http.get(`${admin}/readyz`);
  if (res.status !== 200) {
    throw new Error(`pod ${name} at ${admin} is not ready: ${res.status} ${res.body}`);
  }
}

// Polls an address until it serves path, and answers whether it ever did.
function waitFor(target, path, budgetMs) {
  const deadline = Date.now() + budgetMs;
  for (;;) {
    const res = http.get(`${target}${path}`, { responseCallback: TOLERATED });
    if (res.status === 200) {
      return true;
    }
    if (Date.now() >= deadline) {
      return false;
    }
    sleep(0.1);
  }
}

function health(admin) {
  const res = http.get(`${admin}/__admin/health`, { responseCallback: TOLERATED });
  if (res.status !== 200) {
    return null;
  }
  const doc = res.json();
  return {
    driver: (doc && doc.store && doc.store.driver) || 'unknown',
    stubs: doc && doc.stubs,
    epoch: doc && doc.epoch,
  };
}

// Sums one Prometheus counter across its label sets. The name has to be
// followed by a space or a brace or the sum silently swallows the neighbouring
// series — mockulus_snapshot_reload_duration_seconds is a prefix of its own
// _sum and _count, and a scrape parsed loosely would report a reload count that
// is really a count plus a total number of seconds.
function sumSeries(lines, name) {
  let total = 0;
  let seen = false;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.length === 0 || line[0] === '#' || line.indexOf(name) !== 0) {
      continue;
    }
    const after = line[name.length];
    if (after !== ' ' && after !== '{') {
      continue;
    }
    const value = Number(line.slice(line.lastIndexOf(' ') + 1));
    if (!isNaN(value)) {
      total += value;
      seen = true;
    }
  }
  return seen ? total : NaN;
}

// The reload counters of SPEC §14.1, which are the record of whether the
// coalescing this scenario leans on actually held. A pod that reloaded roughly
// once a second spent the storm doing what §7.4 says it does; a pod whose
// reload count tracks the write count instead is the failure this run exists to
// catch, and it would show up here long before it showed up as a missed p99.
function reloads(admin) {
  const res = http.get(`${admin}/metrics`, { responseCallback: TOLERATED });
  if (res.status !== 200) {
    return null;
  }
  const lines = String(res.body).split('\n');
  const count = sumSeries(lines, 'mockulus_snapshot_reloads_total');
  if (isNaN(count)) {
    return null;
  }
  const spent = sumSeries(lines, 'mockulus_snapshot_reload_duration_seconds_sum');
  const done = sumSeries(lines, 'mockulus_snapshot_reload_duration_seconds_count');
  // An absent histogram is reported as nothing rather than as NaN, because this
  // object travels from setup to teardown through JSON and NaN arrives there as
  // null, which subtracts as zero and would turn a missing scrape into a mean
  // reload time somebody could believe.
  const timed = !isNaN(spent) && !isNaN(done);
  return { count: count, spent: timed ? spent : 0, done: timed ? done : 0 };
}

// Removes every stub tagged with this run's suite, and keeps going until a call
// finds nothing left. remove-by-metadata considers only stubs that carry
// metadata (deviation #20), so nothing another scenario or another runner owns
// is in range of it.
//
// It runs against both admin addresses even though one call clears the whole
// deployment on a shared store, because the one arrangement where that is not
// true is the one someone will misconfigure: two pods started on the memory
// store share nothing, each holds half the storm, and a sweep of pod A alone
// leaves pod B carrying thousands of stubs into whatever runs next. On a
// Couchbase rig the second call is one round trip that removes nothing.
function sweep() {
  let removed = 0;
  const admins = [ADMIN_A, ADMIN_B];
  for (let i = 0; i < admins.length; i++) {
    for (let round = 1; ; round++) {
      const res = http.post(
        `${admins[i]}/__admin/mappings/remove-by-metadata`,
        JSON.stringify({ matchesJsonPath: { expression: '$.suite', equalTo: SUITE } }),
        JSON_HEADERS
      );
      if (res.status !== 200) {
        throw new Error(
          `could not clear the S10 stubs on ${admins[i]}: ${res.status} ${res.body} — every other scenario shares this rig, so they have to go before the next run`
        );
      }
      // The removal answers with the mappings it removed, which WireMock does
      // not and SPEC §5.1 catalogues as an extension. It is what lets this loop
      // tell "nothing left" from "nothing done", and after a storm it is
      // thousands of documents, so the body is parsed once rather than per
      // field read off it.
      const doc = res.json() || {};
      const gone = doc.meta && typeof doc.meta.total === 'number'
        ? doc.meta.total
        : (doc.mappings || []).length;
      removed += gone;
      if (gone === 0) {
        break;
      }
      if (round >= 5) {
        throw new Error(
          `remove-by-metadata on ${admins[i]} is still finding stubs tagged ${SUITE} after ${round} rounds — either another S10 run is registering them concurrently or they are not being deleted`
        );
      }
    }
  }
  return removed;
}

// Aborts the run, having first taken back whatever setup already registered.
// k6 does not call teardown when setup throws, so a bare throw after the mock
// stub is registered leaves it on a rig every other scenario shares.
function fail(message) {
  let note = message;
  try {
    sweep();
  } catch (err) {
    note += ` (and the cleanup that followed it failed too: ${err.message})`;
  }
  throw new Error(note);
}

export function setup() {
  if (POD_A === POD_B || ADMIN_A === ADMIN_B) {
    throw new Error('S10 storms two pods: POD_A/POD_B and ADMIN_A/ADMIN_B must address different instances');
  }
  requireReady('A', ADMIN_A);
  requireReady('B', ADMIN_B);

  // A run that died before its teardown left its stub set behind, and this run
  // would then storm on top of it and report rebuild costs for a stub count
  // nobody wrote down.
  const stale = sweep();
  if (stale > 0) {
    console.warn(`S10: cleared ${stale} stubs left behind by an earlier run before starting`);
  }

  const run = Date.now().toString(36);
  const base = `${NS}${run}`;
  const path = `${base}/resource`;

  const created = http.post(`${ADMIN_A}/__admin/mappings`, JSON.stringify(stubFor(path)), JSON_HEADERS);
  if (created.status !== 201) {
    fail(`could not register the S10 mock stub: ${created.status} ${created.body}`);
  }

  // That one registration answers both questions the topology raises, in the
  // order they have to be asked. Pod B cannot be serving it yet: a genuine peer
  // has to poll the epoch before it rebuilds, and only the pod that took the
  // write splices it in immediately (SPEC §4.3 step 5), so a 200 in the
  // millisecond after the 201 means both addresses reach one pod — and a storm
  // "across 2 pods" measured that way is a storm against one.
  const immediate = http.get(`${POD_B}${path}`, { responseCallback: TOLERATED });
  if (immediate.status === 200) {
    fail('POD_B served a stub the instant POD_A took it, so both addresses reach the same pod and the storm would not be spread across two');
  }

  // And then it has to arrive, or the two pods are two deployments that happen
  // to be running next to each other: nothing propagates, no peer reloads, and
  // the serve-side half of this scenario has nothing to be disturbed by.
  if (!waitFor(POD_B, path, 30000)) {
    fail('the stub registered on POD_A never reached POD_B — check that both pods share one Couchbase bucket and scope, and that sync_interval is not longer than 30 s');
  }
  if (!waitFor(BASE, path, 30000)) {
    fail(`the S10 mock stub is not servable at ${BASE}`);
  }

  const a = health(ADMIN_A);
  const b = health(ADMIN_B);
  const driver = a ? a.driver : 'unknown';
  console.log(
    `S10: ${MODE} mode, storm ${STORM_RATE}/s for ${STORM_SECONDS}s across two pods, store driver ${driver}, ${a ? a.stubs : '?'} stubs on A and ${b ? b.stubs : '?'} on B`
  );
  if (driver !== 'couchbase') {
    console.warn(
      `S10: the ${driver} store takes admin writes in process, so the create latency this run reports is not the SPEC §16.1 S10 number — record the driver with it`
    );
  }

  return { path: path, base: base, reloadsA: reloads(ADMIN_A), reloadsB: reloads(ADMIN_B) };
}

// The S1 half: one stub, exact URL, GET, 256 B body — deliberately identical to
// s1.js so that the only difference between the two p99s is the storm.
export function mockLoad(data) {
  const res = http.get(`${BASE}${data.path}`, { tags: { op: 'mock' } });
  check(
    res,
    {
      'mock status is 200': (r) => r.status === 200,
      'mock body is intact': (r) => r.body.length === 256,
    },
    { op: 'mock' }
  );
}

// The storm half: one create per iteration, alternating pods so the 100 a
// second of the SLO is split evenly rather than aimed at whichever pod k6
// happened to open a connection to first.
export function adminStorm(data) {
  const iteration = exec.scenario.iterationInTest;
  const onA = iteration % 2 === 0;
  const admin = onA ? ADMIN_A : ADMIN_B;
  const pod = onA ? 'a' : 'b';

  const path = `${data.base}/storm/${iteration}`;
  const res = http.post(`${admin}/__admin/mappings`, JSON.stringify(stubFor(path)), {
    headers: { 'Content-Type': 'application/json' },
    tags: { op: 'create', pod: pod },
  });

  // Timed on the tagged trend rather than read off http_req_duration, so the
  // threshold names the create and nothing else can drift into it, and tagged
  // by pod so a rig with one slow node is legible rather than just worse.
  createDuration.add(res.timings.duration, { pod: pod });
  if (check(res, { 'create returned 201': (r) => r.status === 201 }, { op: 'create' })) {
    creates.add(1, { pod: pod });
  }
}

// What the run did to one pod's reload loop, as a line somebody can copy into
// BASELINE.md next to the percentiles.
function report(name, before, after) {
  if (!before || !after) {
    console.log(`S10: reload counters for pod ${name} unavailable — /metrics is served on the admin port only, so pass the admin addresses to have them in the summary`);
    return;
  }
  const count = after.count - before.count;
  const done = after.done - before.done;
  const spent = after.spent - before.spent;
  const mean = done > 0 ? ((spent / done) * 1000).toFixed(1) : '?';
  console.log(
    `S10: pod ${name} reloaded ${count} times during the run, ${mean} ms mean — against ${STORM_SECONDS}s of storming at ${STORM_RATE} writes/s`
  );
}

export function teardown(data) {
  // Read while the stubs are still there: after the sweep both pods rebuild and
  // the counts below stop describing the run.
  const a = health(ADMIN_A);
  const b = health(ADMIN_B);
  if (a && b) {
    console.log(`S10: ended with ${a.stubs} stubs at epoch ${a.epoch} on A and ${b.stubs} at epoch ${b.epoch} on B`);
  }

  const afterA = reloads(ADMIN_A);
  const afterB = reloads(ADMIN_B);
  report('A', data && data.reloadsA, afterA);
  report('B', data && data.reloadsB, afterB);

  console.log(`S10: removed ${sweep()} stubs`);
}
