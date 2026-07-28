// SPDX-License-Identifier: Apache-2.0
//
// S9 — the propagation scenario of SPEC §16.1: a stub registered on pod A must
// be serving on pod B within sync_interval + a warm reload + 200 ms, which at
// defaults with 10k stubs is 1.5 s. The SLO names two measurement points, 1k
// and 10k stubs, so the stub set is a knob and the run is repeated.
//
// This is the number a test written against a multi-pod deployment lives on:
// register a stub, call the Service, get your own stub back. It cannot be
// measured on one pod. The pod that handles the admin write splices the stub
// into its own snapshot before it answers 201 (SPEC §4.3 step 5), so a
// single-pod rig reports a propagation delay of roughly zero and passes this
// script without having tested anything — which is what setup() spends its
// first requests ruling out. The rig therefore needs two mockulus pods sharing
// one Couchbase bucket; the memory store has nothing to propagate through.
//
//   k6 run -e POD_A=http://a:8080 -e ADMIN_A=http://a:9090 \
//          -e POD_B=http://b:8080 -e ADMIN_B=http://b:9090 \
//          -e STUBS=10000 test/load/s9.js
//   k6 run ... -e STUBS=1000 test/load/s9.js     # the other measurement point

import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Trend, Counter } from 'k6/metrics';

const POD_A = (__ENV.POD_A || 'http://localhost:8080').replace(/\/+$/, '');
const POD_B = (__ENV.POD_B || 'http://localhost:8081').replace(/\/+$/, '');
const ADMIN_A = (__ENV.ADMIN_A || 'http://localhost:9090').replace(/\/+$/, '');
const ADMIN_B = (__ENV.ADMIN_B || 'http://localhost:9091').replace(/\/+$/, '');

// The stub count the propagation is measured at — a reload rebuilds all of
// them, so this is the dominant term in the budget the SLO writes down.
const STUBS = Number(__ENV.STUBS || 10000);
const SAMPLES = Number(__ENV.SAMPLES || 30);

// The measurement is quantised to the poll interval and always rounds against
// us, so the only cost of polling B this hard is the error being 20 ms of a
// 1500 ms budget rather than something worth arguing about.
const POLL_MS = Number(__ENV.POLL_MS || 20);

// How long a sample waits before it is called a failure rather than a slow
// propagation, and how long the deployment is left alone between samples so
// each one starts from a settled cluster rather than from the reload the
// previous sample's cleanup delete triggered.
const DEADLINE_MS = Number(__ENV.DEADLINE_MS || 10000);
const SETTLE_MS = Number(__ENV.SETTLE_MS || 2000);

// The rig's own sync_interval, which the script cannot read off the pods and
// therefore has to be told. It exists to gate the threshold below: 1.5 s is
// the budget at defaults, and a deployment that has tuned the poller is still
// worth measuring but is not the release criterion.
const SYNC_INTERVAL_MS = Number(__ENV.SYNC_INTERVAL_MS || 1000);

const IMPORT_BATCH = Number(__ENV.IMPORT_BATCH || 500);

// Every URL this scenario touches lives under one prefix, which is what makes
// the cleanup in teardown() targeted enough to run on a rig shared with the
// other scenarios.
const NS = '/load/s9/';
const BODY_256 = 'x'.repeat(256);

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

// A 404 from pod B is the expected answer for as long as the stub is still in
// flight, so the poll says so rather than letting the http_req_failed
// threshold below degrade into noise that never fails.
const POLL_PARAMS = { responseCallback: http.expectedStatuses(200, 404) };
const DELETE_PARAMS = { responseCallback: http.expectedStatuses(200, 204, 404) };

const propagation = new Trend('s9_propagation_delay', true);
const adminWrite = new Trend('s9_admin_write', true);
const timeouts = new Counter('s9_propagation_timeouts');

// The 1.5 s of SPEC §16.1 is stated for the default poller and 10k stubs, in
// the same way S1's p99 is stated at 20k RPS, so the threshold applies at that
// configuration and a run outside it records numbers without gating on them.
const AT_DEFAULTS = SYNC_INTERVAL_MS === 1000 && STUBS <= 10000;

export const options = {
  scenarios: {
    s9_propagation: {
      // One registration at a time, by one VU. Propagation is a property of
      // the deployment rather than of load, and concurrent registrations would
      // coalesce into shared reloads on B (SPEC §8) — every sample after the
      // first would then be measuring a rebuild it did not pay for.
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: SAMPLES,
      maxDuration: __ENV.DURATION || '20m',
    },
  },
  setupTimeout: __ENV.SETUP_TIMEOUT || '10m',
  teardownTimeout: __ENV.TEARDOWN_TIMEOUT || '10m',
  thresholds: {
    'http_req_failed': ['rate==0'],
    'checks': ['rate==1'],
    // A sample that never arrived would otherwise sit in the trend as one
    // large number among many small ones; it fails the run on its own.
    's9_propagation_timeouts': ['count==0'],
    // The SLO is a bound on a registration becoming visible, not a percentile
    // over registrations, so the threshold is the maximum: one propagation
    // past 1.5 s is a missed release criterion however good the others were.
    ...(AT_DEFAULTS ? { 's9_propagation_delay': ['max<1500'] } : {}),
  },
};

function stubFor(path) {
  return {
    request: { method: 'GET', urlPath: path },
    response: {
      status: 200,
      body: BODY_256,
      headers: { 'Content-Type': 'text/plain' },
    },
  };
}

// Polls pod B until it serves path, and answers how long that took measured
// from t0. A negative answer means the budget ran out first.
function firstVisible(path, t0, budgetMs) {
  const deadline = t0 + budgetMs;
  for (;;) {
    const res = http.get(`${POD_B}${path}`, POLL_PARAMS);
    const now = Date.now();
    if (res.status === 200) {
      return { delay: now - t0, res: res };
    }
    if (now >= deadline) {
      return { delay: -1, res: res };
    }
    sleep(POLL_MS / 1000);
  }
}

function requireReady(name, admin) {
  const res = http.get(`${admin}/readyz`);
  if (res.status !== 200) {
    throw new Error(`pod ${name} at ${admin} is not ready: ${res.status} ${res.body}`);
  }
}

function listNamespace(admin) {
  const ids = [];
  const page = 500;
  for (let offset = 0; ; offset += page) {
    const res = http.get(`${admin}/__admin/mappings?limit=${page}&offset=${offset}`);
    if (res.status !== 200) {
      throw new Error(`could not list mappings on ${admin}: ${res.status} ${res.body}`);
    }
    const mappings = JSON.parse(res.body).mappings || [];
    for (const m of mappings) {
      const target = (m.request && (m.request.urlPath || m.request.url)) || '';
      if (target.indexOf(NS) === 0) {
        ids.push(m.id);
      }
    }
    if (mappings.length < page) {
      return ids;
    }
  }
}

function removeAll(admin, ids) {
  const width = 20;
  for (let i = 0; i < ids.length; i += width) {
    const batch = [];
    for (const id of ids.slice(i, i + width)) {
      batch.push({ method: 'DELETE', url: `${admin}/__admin/mappings/${id}`, params: DELETE_PARAMS });
    }
    http.batch(batch);
  }
}

// Removes everything under the scenario's prefix, one id at a time. The bulk
// endpoints (DELETE /__admin/mappings, POST /__admin/reset) would be one call
// instead of thousands, but they are cluster-wide: on a rig shared with the
// other S# scripts they clear stubs this scenario never registered. Ids are
// collected in full before the first delete, because a page read after a
// delete has shifted under the offset, and a shifted page is how a sweep
// leaves stubs behind.
function sweep(admin) {
  let ids = listNamespace(admin);
  for (let round = 0; ids.length > 0 && round < 3; round++) {
    removeAll(admin, ids);
    ids = listNamespace(admin);
  }
  if (ids.length > 0) {
    throw new Error(`${ids.length} stubs under ${NS} survived cleanup on ${admin} and the next scenario would run against them`);
  }
}

export function setup() {
  if (STUBS < 1) {
    throw new Error('STUBS must be at least 1 — the SLO is measured at 1k and 10k');
  }
  if (POD_A === POD_B || ADMIN_A === ADMIN_B) {
    throw new Error('S9 needs two pods: POD_A/POD_B and ADMIN_A/ADMIN_B must address different instances');
  }
  requireReady('A', ADMIN_A);
  requireReady('B', ADMIN_B);

  // A run that died before its teardown leaves its stub set behind, and this
  // run would then quietly measure 20k stubs while reporting 10k.
  sweep(ADMIN_A);

  const run = Date.now().toString(36);
  const base = `${NS}${run}`;

  for (let first = 0; first < STUBS; first += IMPORT_BATCH) {
    const last = Math.min(first + IMPORT_BATCH, STUBS);
    const mappings = [];
    for (let i = first; i < last; i++) {
      mappings.push(stubFor(`${base}/fill/${i}`));
    }
    const res = http.post(`${ADMIN_A}/__admin/mappings/import`, JSON.stringify({ mappings }), JSON_HEADERS);
    if (res.status !== 200) {
      throw new Error(`could not import S9 stubs ${first}..${last}: ${res.status} ${res.body}`);
    }
  }

  // The last stub of the last batch is both the single-pod tripwire and the
  // signal that B has caught up. Immediately after the import returns, B can
  // only be serving it if B is the pod that handled the write: a genuine peer
  // has to poll the epoch (sync_interval, 1 s at defaults) and then rebuild
  // the whole stub set, and no rebuild of 10k stubs finishes inside the
  // millisecond between these two requests.
  const sentinel = `${base}/fill/${STUBS - 1}`;
  const immediate = http.get(`${POD_B}${sentinel}`, POLL_PARAMS);
  if (immediate.status === 200) {
    throw new Error('POD_B served a stub the instant POD_A took it, so both addresses reach the same pod; measured that way S9 reports zero and passes without testing propagation at all');
  }

  // Waiting for that first convergence is what makes the samples below
  // measure a warm reload. B's first rebuild after the import compiles every
  // stub from an empty cache, which is S7's cold number rather than S9's, and
  // it is also the point at which B's compile cache stops being empty.
  const warmup = firstVisible(sentinel, Date.now(), 120000);
  if (warmup.delay < 0) {
    throw new Error('the imported stub set never reached POD_B — check that both pods share one Couchbase bucket and scope');
  }

  return { base: base };
}

export default function (data) {
  const path = `${data.base}/probe/${exec.scenario.iterationInTest}`;

  const created = http.post(`${ADMIN_A}/__admin/mappings`, JSON.stringify(stubFor(path)), JSON_HEADERS);
  // The clock starts where the SLO starts it: the registration exists once A
  // has acknowledged it, and everything after that is the propagation path.
  // The write itself is recorded separately so the two are never confused.
  const t0 = Date.now();
  adminWrite.add(created.timings.duration);
  if (!check(created, { 'probe registered on A': (r) => r.status === 201 })) {
    return;
  }

  const seen = firstVisible(path, t0, DEADLINE_MS);
  if (seen.delay < 0) {
    timeouts.add(1);
    propagation.add(DEADLINE_MS);
  } else {
    propagation.add(seen.delay);
    check(seen.res, { 'pod B served the registered body': (r) => r.body.length === 256 });
  }

  http.del(`${ADMIN_A}/__admin/mappings/${JSON.parse(created.body).id}`);
  sleep(SETTLE_MS / 1000);
}

export function teardown() {
  sweep(ADMIN_A);
}
