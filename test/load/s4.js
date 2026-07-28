// SPDX-License-Identifier: Apache-2.0
//
// S4 — the scenario-stub scenario of SPEC §16.1: every request matches a stub
// gated on scenario state, so the request path performs one state read per
// request. Target on the reference rig (1 pod, 2 vCPU, 512Mi): p99 < 8 ms at
// 5k RPS, with a 256 B response body.
//
// This is the one scenario whose number is not really about mockulus. A gated
// stub costs a KV get before it can match (SPEC §9.2), and against the
// microseconds an exact URL match costs, that get is the response time. It is
// why the budget is 8 ms where S1's is 2 ms, and why the number only means
// something with Couchbase behind the pod: run this against the memory store
// and the state read is a map lookup, so what comes back is the cost of the
// gate itself — real, worth knowing, and not this SLO. setup() prints the store
// driver that answered so a result cannot be filed under the wrong heading, and
// BASELINE.md records the driver next to the number.
//
// There is deliberately no throughput target. The ceiling here belongs to
// Couchbase rather than to us, so the release criterion is latency at a stated
// rate — which only holds if the rate was actually delivered, hence the
// dropped-iteration threshold below.
//
//   k6 run test/load/s4.js
//   k6 run -e BASE=http://host:8080 -e ADMIN=http://host:9090 test/load/s4.js
//   k6 run -e RATE=8000 -e DURATION=5m test/load/s4.js      # past the SLO point

import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || BASE;

const RATE = Number(__ENV.RATE || 5000);
const DURATION = __ENV.DURATION || '60s';

// The read pool is spread over many scenario names rather than many paths in
// one scenario, because a scenario is one document (SPEC §9.1) and one document
// is one vbucket on one node. Reading a single key would measure how well that
// node keeps it hot, which is a much prettier number than what a deployment
// with a few hundred flows in it actually pays.
const SCENARIOS = Number(__ENV.SCENARIOS || 200);

// The transitioning half. WALK_EVERY is how often an iteration advances a state
// machine instead of only reading one; 0 turns the walk off entirely.
const WALKS = Number(__ENV.WALKS || 32);
const WALK_EVERY = Number(__ENV.WALK_EVERY || 10);

// Every stub this script registers carries this in its metadata, so teardown
// can remove exactly what it created and nothing a concurrent run owns. The
// global resets are not an option on a shared deployment (SPEC §2).
const SUITE = __ENV.SUITE || 'load-s4';

// The state machine, kept to three states and closed into a ring: the stub that
// serves the last one sends the scenario back to Started. That is the whole
// re-arming mechanism — a run of any length walks the same three transitions
// round and round instead of needing a state per iteration, and it never has to
// stop and reset itself mid-measurement.
const RING = ['Started', 'ordered', 'shipped'];

// Both of these would produce a run that reports a p99 and measures nothing, so
// they are refused at init rather than at the first confusing 404.
if (SCENARIOS < 1) {
  throw new Error('SCENARIOS must be at least 1: the read pool is what the SLO is stated over');
}
if (WALK_EVERY === 1) {
  throw new Error(
    'WALK_EVERY=1 leaves no read traffic for the p99 threshold to gate; pass 0 to turn the walk off'
  );
}
const WALKING = WALKS > 0 && WALK_EVERY > 1;

// A 404 is an expected answer while setup waits for a freshly imported stub to
// reach the snapshot, and /metrics is expected to be absent when ADMIN points
// at the mock port. Neither is a failure of the deployment, and neither should
// show up in the run summary as one.
const TOLERATED = http.expectedStatuses(200, 404);

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

export const options = {
  scenarios: {
    s4_scenario_state: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Number(__ENV.VUS || 200),
      maxVUs: Number(__ENV.MAX_VUS || 2000),
    },
  },
  thresholds: {
    'http_req_failed{op:read}': ['rate==0'],
    checks: ['rate==1'],

    // The measurement point is the rate, so a run that could not keep up has
    // not measured the SLO however good its p99 looks.
    dropped_iterations: ['count==0'],

    // The SLO ties p99 < 8 ms to 5k RPS, and it is stated over the state *read*:
    // transitions are gated separately below and deliberately not timed here.
    ...(RATE <= 5000 ? { 'http_req_duration{op:read}': ['p(99)<8'] } : {}),

    // The walk has no latency target in §16.1 and gets none invented for it. It
    // still has to serve: a ring where every state has a stub answers whatever
    // state the last request left behind, so anything but a 200 means the state
    // machine went somewhere no stub covers.
    ...(WALKING ? { 'http_req_failed{op:walk}': ['rate==0'] } : {}),
  },
};

const BODY_LEN = 256;

// The reference rig specifies a 256 B body. The state that served is spelled in
// the first bytes of it, so a run that goes wrong can be read rather than
// guessed at from a status code.
function body(marker) {
  return marker + 'x'.repeat(BODY_LEN - marker.length);
}

function readName(i) {
  return `s4-read-${i}`;
}

function walkName(i) {
  return `s4-walk-${i}`;
}

function readPath(i) {
  return `/load/s4/read/${i}`;
}

function walkPath(i) {
  return `/load/s4/walk/${i}`;
}

function stub(scenarioName, requiredState, newState, path, marker) {
  const doc = {
    scenarioName: scenarioName,
    requiredScenarioState: requiredState,
    metadata: { suite: SUITE },
    request: { method: 'GET', urlPath: path },
    response: {
      status: 200,
      body: body(marker),
      headers: { 'Content-Type': 'text/plain' },
    },
  };
  if (newState) {
    doc.newScenarioState = newState;
  }
  return doc;
}

// mappings builds the stub set. Matching stays at S1's shape on purpose —
// exact URL paths, no patterns, no templating — so the difference between the
// two numbers is the store round trip and not a matcher the scenario happened
// to drag in.
function mappings() {
  const docs = [];

  for (let i = 0; i < SCENARIOS; i++) {
    docs.push(stub(readName(i), 'Started', null, readPath(i), 'read'));
  }

  for (let i = 0; i < WALKS; i++) {
    for (let j = 0; j < RING.length; j++) {
      const from = RING[j];
      const to = RING[(j + 1) % RING.length];
      docs.push(stub(walkName(i), from, to, walkPath(i), from));
    }
  }

  return docs;
}

function scenarioNames() {
  const names = [];
  for (let i = 0; i < SCENARIOS; i++) {
    names.push(readName(i));
  }
  for (let i = 0; i < WALKS; i++) {
    names.push(walkName(i));
  }
  return names;
}

// arm writes a scenario's state explicitly rather than trusting the default,
// and it is needed twice for two different reasons.
//
// State outlives the stubs that define it: a scenario exists because a stub
// mentions it, but its state is a stored document, so an earlier run's leftover
// state survives that run deleting its stubs and greets the next one. Arming in
// setup is also what makes the read pool measure a KV hit — an absent document
// reads as Started (SPEC §9.1), which is the right answer and the wrong
// measurement, because a miss is not what a deployment in use pays for.
function arm(name, state) {
  return http.put(
    `${ADMIN}/__admin/scenarios/${name}/state`,
    JSON.stringify({ state: state }),
    JSON_HEADERS
  );
}

// The one matcher this run cleans up by, and the one it reads its own import
// back through, so the tag teardown depends on is proven to be on the stubs
// before anything leans on it.
function suiteMatcher() {
  return JSON.stringify({
    matchesJsonPath: { expression: '$.suite', equalTo: SUITE },
  });
}

function removeSuite() {
  return http.post(
    `${ADMIN}/__admin/mappings/remove-by-metadata`,
    suiteMatcher(),
    JSON_HEADERS
  );
}

// Anything that fails after the import has to take the import with it: a throw
// out of setup does not run teardown, and a few hundred scenario stubs left
// behind become the next scenario's mystery rather than this one's failure.
function abort(message) {
  removeSuite();
  throw new Error(message);
}

// counters pulls the scenario counters out of a Prometheus scrape. They are the
// contention record of SPEC §9.5, and without them a run that met p99 while
// burning CAS retries is indistinguishable from one that did not — which is the
// difference between a headroom number and a number that is about to stop being
// true.
function counters() {
  const res = http.get(`${ADMIN}/metrics`, { responseCallback: TOLERATED });
  if (res.status !== 200) {
    return null;
  }
  const lines = String(res.body).split('\n');
  const value = (name) => {
    const line = lines.find((l) => l.indexOf(name + ' ') === 0);
    return line ? Number(line.slice(name.length + 1)) : NaN;
  };
  const out = {
    reads: value('mockulus_scenario_reads_total'),
    retries: value('mockulus_scenario_cas_retries_total'),
    conflicts: value('mockulus_scenario_transition_conflicts_total'),
  };
  if (isNaN(out.reads) || isNaN(out.retries) || isNaN(out.conflicts)) {
    return null;
  }
  return out;
}

export function setup() {
  const docs = mappings();

  // One import rather than a stub at a time: every admin write bumps the epoch
  // and rebuilds the snapshot, and a few hundred rebuilds back to back would
  // measure a pod that had just spent the run rebuilding.
  const res = http.post(
    `${ADMIN}/__admin/mappings/import`,
    JSON.stringify({ mappings: docs }),
    JSON_HEADERS
  );
  if (res.status !== 200) {
    abort(`could not import the S4 stubs: ${res.status} ${res.body}`);
  }

  // The import answers 200 with no body, so what landed is read back through
  // the matcher teardown will remove by. A count that disagrees means either a
  // short import or stubs an earlier run left tagged, and both want a person.
  const found = http.post(
    `${ADMIN}/__admin/mappings/find-by-metadata`,
    suiteMatcher(),
    JSON_HEADERS
  );
  if (found.status !== 200) {
    abort(`could not read the S4 stubs back: ${found.status} ${found.body}`);
  }
  const total = JSON.parse(found.body).meta.total;
  if (total !== docs.length) {
    abort(
      `expected ${docs.length} stubs tagged ${SUITE}, found ${total}; ` +
        'clear them with POST /__admin/mappings/remove-by-metadata and run again'
    );
  }

  // An import is visible to the pod that took it as soon as it rebuilds, but a
  // deployment behind a Service can answer this from a pod that is still a
  // sync_interval away from having seen it, so readiness is polled rather than
  // assumed. It is also what makes the state writes below safe: a state can
  // only be set on a scenario the snapshot already defines (SPEC §9.4).
  let serving = null;
  for (let i = 0; i < 40; i++) {
    serving = http.get(`${BASE}${readPath(0)}`, { responseCallback: TOLERATED });
    if (serving.status === 200) {
      break;
    }
    sleep(0.5);
  }
  if (!serving || serving.status !== 200) {
    abort(
      `the S4 stubs never became servable: ${serving ? serving.status : 'no response'}`
    );
  }

  const names = scenarioNames();
  for (let i = 0; i < names.length; i++) {
    const armed = arm(names[i], 'Started');
    if (armed.status !== 200) {
      abort(`could not arm scenario ${names[i]}: ${armed.status} ${armed.body}`);
    }
  }

  let driver = 'unknown';
  const health = http.get(`${ADMIN}/__admin/health`);
  if (health.status === 200) {
    const doc = health.json();
    if (doc && doc.store && doc.store.driver) {
      driver = doc.store.driver;
    }
  }
  console.log(
    `S4: ${docs.length} stubs, ${SCENARIOS} read scenarios, ${WALKS} rings, store driver ${driver}`
  );
  if (driver !== 'couchbase') {
    console.warn(
      `S4: the ${driver} store answers state reads in process, so this run is not the SPEC §16.1 S4 number — record the driver with it`
    );
  }

  return { before: counters() };
}

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const slot = WALKING ? iteration % WALK_EVERY : 1;
  const round = WALKING ? Math.floor(iteration / WALK_EVERY) : iteration;

  if (WALKING && slot === 0) {
    // Transitions are a minority of the traffic on purpose. A scenario's state
    // is one document and its transitions serialize through CAS, which SPEC
    // §9.5 puts a per-name ceiling of a few thousand a second on; pushing the
    // whole rate through them would measure that ceiling instead of the read
    // the SLO names. Spread over WALKS rings at one iteration in WALK_EVERY,
    // the defaults leave each ring taking a few transitions a second.
    const res = http.get(`${BASE}${walkPath(round % WALKS)}`, {
      tags: { op: 'walk' },
    });
    check(res, {
      'walk status is 200': (r) => r.status === 200,
      'walk body is intact': (r) => r.body.length === BODY_LEN,
    });
    return;
  }

  // A dense index over the read iterations rather than the raw counter. The
  // walk takes every WALK_EVERY-th iteration, so iteration % SCENARIOS would
  // never land on a name whose index is a multiple of WALK_EVERY: a twentieth
  // of the pool would sit cold at the defaults, and the keys that did get read
  // would be read that much harder than the run claims.
  const index = WALKING ? round * (WALK_EVERY - 1) + (slot - 1) : iteration;
  const res = http.get(`${BASE}${readPath(index % SCENARIOS)}`, {
    tags: { op: 'read' },
  });
  check(res, {
    'read status is 200': (r) => r.status === 200,
    'read body is intact': (r) => r.body.length === BODY_LEN,
  });
}

export function teardown(data) {
  const after = counters();
  if (data && data.before && after) {
    console.log(
      `S4: ${after.reads - data.before.reads} scenario state reads, ` +
        `${after.retries - data.before.retries} CAS retries, ` +
        `${after.conflicts - data.before.conflicts} transition conflicts`
    );
  } else {
    console.log(
      'S4: scenario counters unavailable — /metrics is served on the admin port only, so pass ADMIN=http://host:9090 to have them in the summary'
    );
  }

  // Put the rings back before the stubs go, in that order. Nothing deletes a
  // single scenario's state — scenarios/reset clears every scenario in the
  // deployment, including ones this run does not own — and once the stubs are
  // gone the scenario is no longer defined and its state can no longer be
  // written. Left at Started, what remains is indistinguishable from the
  // absence the next run expects.
  for (let i = 0; i < WALKS; i++) {
    arm(walkName(i), 'Started');
  }

  const removed = removeSuite();
  if (removed.status !== 200) {
    throw new Error(
      `the S4 stubs are still registered: remove-by-metadata answered ${removed.status} ${removed.body} — every other scenario shares this rig, so clear them before the next run`
    );
  }
}
