// SPDX-License-Identifier: Apache-2.0
//
// S8 — the memory criterion of SPEC §16.1: under 256 MiB RSS while the S2 stub
// set and traffic mix are served, including across a snapshot reload of the
// kind S7 measures, and with no growth over a one hour soak.
//
// The S2 shape is rebuilt here under /load/s8/ rather than borrowed from s2.js.
// Every scenario owns the stubs it drives, so two of them sharing a rig can
// never disagree about what is registered, and a failed S8 run cannot leave S2
// holding stubs it did not create.
//
// RSS is the one quantity in the SLO table that k6 cannot see: k6 measures the
// client end of a connection and the criterion is about the server process. So
// the reading comes from the process collector on the ops port and is polled by
// a second, deliberately slow scenario running beside the load. Keeping the
// poll inside the same run is what makes a breach attributable — the sample
// that crossed the ceiling and the traffic that provoked it land in one
// summary, instead of in two graphs somebody has to line up afterwards.
//
// The other half of the target, no growth over an hour, is not expressible as a
// threshold. A threshold judges the distribution of a metric across a whole run
// and knows nothing about when a sample was taken, so `max<256` reads a flat
// 200 MiB and a slow climb from 120 MiB to 250 MiB as the same pass. The leak
// gate therefore lives in test/load/s8-driver.sh, which keeps the series in
// time order and compares its first decile against its last. This script owns
// the ceiling; the driver owns the trend.
//
// What this does not do is re-gate S2. Throughput and p99 belong to s2.js and
// are asserted there; here they are only the load that makes the memory reading
// mean something.
//
//   k6 run test/load/s8.js                                  # ceiling at S2's p99 point
//   k6 run -e RATE=30000 -e DURATION=10m test/load/s8.js    # ceiling at S2's throughput target
//   k6 run -e BASE=http://host:8080 -e OPS=http://host:9090 test/load/s8.js
//   test/load/s8-driver.sh                                  # the 1 h leak gate

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Trend, Counter } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || BASE;
// The admin API is served on the mock port by default, which is why s1 can let
// ADMIN fall back to BASE. /metrics never is (SPEC §14.3), so the sampler needs
// the ops port named separately — and on a Kubernetes rig that port has to be
// reachable from wherever k6 runs, not only from the kubelet.
const OPS = __ENV.OPS || 'http://localhost:9090';

const RATE = Number(__ENV.RATE || 15000);
const DURATION = __ENV.DURATION || '5m';
const STUBS = Number(__ENV.STUBS || 1000);
const SAMPLE_EVERY = __ENV.SAMPLE_EVERY || '2s';
const RELOAD_EVERY = __ENV.RELOAD_EVERY || '60s';
const RELOAD_MODE = __ENV.RELOAD_MODE || 'cold';

// S2's mix: 70% exact URL, 20% regex, 10% JSONPath on the body.
const EXACT = Math.round(STUBS * 0.7);
const PATTERN = Math.round(STUBS * 0.2);
const JSONBODY = STUBS - EXACT - PATTERN;

const MIB = 1024 * 1024;
const BODY = 256;

// One tag for the whole scenario, so teardown can clear exactly what setup
// registered without knowing which run wrote it. It is also what lets a crashed
// run be cleaned up by the next one instead of by hand.
const METADATA = { harness: 'load', scenario: 's8' };

const rssMiB = new Trend('rss_mib');
const heapMiB = new Trend('heap_mib');
const rssSamples = new Counter('rss_samples');
const reloadsDone = new Counter('reloads_done');

const RSS_LINE = /^process_resident_memory_bytes (\S+)$/m;
const HEAP_LINE = /^go_memstats_heap_inuse_bytes (\S+)$/m;

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };
const LOAD_GET = { tags: { probe: 'load' } };
const LOAD_POST = { headers: { 'Content-Type': 'application/json' }, tags: { probe: 'load' } };
const SAMPLE_PARAMS = { tags: { probe: 'sample' } };
const RELOAD_PARAMS = {
  headers: { 'Content-Type': 'application/json' },
  tags: { probe: 'reload' },
  timeout: '60s',
};

// The ceiling is claimed for S2 — that stub set, at or below the rate S2 is
// required to sustain. A bigger set is the S6/S7 shape and a harder rate is
// past the envelope the SLO describes, so in either case the run still records
// the number but stops calling it a release criterion, the same way s1 stops
// asserting p99 above 20k.
const AT_S2_SHAPE = STUBS <= 1000 && RATE <= 30000;

export const options = {
  scenarios: {
    s8_load: {
      executor: 'constant-arrival-rate',
      exec: 'load',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 200,
      maxVUs: 2000,
    },
    // One scrape every SAMPLE_EVERY, on its own VUs, so a slow scrape delays
    // the next sample rather than stealing an iteration from the load.
    //
    // Two seconds does not resolve a rebuild, which finishes in milliseconds,
    // and does not need to. Resident memory is not what the process allocated
    // but what the kernel has not taken back, and the Go scavenger returns
    // pages over minutes — so the mark a rebuild leaves outlives the rebuild by
    // orders of magnitude. The interval has to be fast against the scavenger,
    // not against the work.
    s8_memory: {
      executor: 'constant-arrival-rate',
      exec: 'sampleMemory',
      rate: 1,
      timeUnit: SAMPLE_EVERY,
      duration: DURATION,
      preAllocatedVUs: 2,
      maxVUs: 4,
    },
    s8_reload: {
      executor: 'constant-arrival-rate',
      exec: 'reloadSnapshot',
      rate: 1,
      timeUnit: RELOAD_EVERY,
      duration: DURATION,
      preAllocatedVUs: 1,
      maxVUs: 2,
    },
  },
  thresholds: {
    // The three request populations are thresholded apart. A scrape that failed
    // is a hole in the measurement and a reload that failed is a reload that
    // never happened, and neither should be diluted into the load's error rate,
    // where a handful of them would round away against millions of requests.
    'http_req_failed{probe:load}': ['rate==0'],
    'http_req_failed{probe:sample}': ['rate==0'],
    'http_req_failed{probe:reload}': ['rate==0'],

    // A threshold over a metric that collected nothing passes, so without these
    // two a run that sampled no memory and reloaded nothing would be reported
    // exactly like a run that met the target. They are the difference between
    // "the ceiling held" and "nobody looked".
    rss_samples: ['count>0'],
    reloads_done: ['count>0'],

    ...(AT_S2_SHAPE ? { rss_mib: ['max<256'] } : {}),
  },
};

// The stub ids are derived from the index rather than assigned by the server.
// The reload has to overwrite the same 1,000 documents to stay a reload — with
// server-assigned ids it would register a second set beside the first and
// measure the memory of 2,000 stubs — and deterministic ids also mean a rerun
// after a crash overwrites the wreckage instead of doubling it.
function stubID(kind, i) {
  return 'f800000' + kind + '-0000-4000-8000-' + ('000000000000' + i).slice(-12);
}

// Every response is the 256 B the reference rig specifies. The generation is
// carried in the body because the compile cache is keyed by (id, content hash)
// per SPEC §6.2: a body that changed is a stub that has to be recompiled.
function body(tag) {
  return (tag + '|' + 'x'.repeat(BODY)).slice(0, BODY);
}

function hex8(i) {
  return ('0000000' + i.toString(16)).slice(-8);
}

function exactStub(i, gen) {
  return {
    id: stubID(1, i),
    metadata: METADATA,
    request: { method: 'GET', urlPath: '/load/s8/exact/' + i },
    response: {
      status: 200,
      body: body('s8/' + gen + '/exact/' + i),
      headers: { 'Content-Type': 'text/plain' },
    },
  };
}

function patternStub(i, gen) {
  return {
    id: stubID(2, i),
    metadata: METADATA,
    request: { method: 'GET', urlPathPattern: '/load/s8/re/' + i + '/[0-9a-f]{8}' },
    response: {
      status: 200,
      body: body('s8/' + gen + '/re/' + i),
      headers: { 'Content-Type': 'text/plain' },
    },
  };
}

function jsonStub(i, gen) {
  return {
    id: stubID(3, i),
    metadata: METADATA,
    request: {
      method: 'POST',
      urlPath: '/load/s8/json/' + i,
      bodyPatterns: [{ matchesJsonPath: { expression: '$.tenant', equalTo: 't' + i } }],
    },
    response: {
      status: 200,
      body: body('s8/' + gen + '/json/' + i),
      headers: { 'Content-Type': 'text/plain' },
    },
  };
}

function stubSet(gen) {
  const mappings = [];
  for (let i = 0; i < EXACT; i++) mappings.push(exactStub(i, gen));
  for (let i = 0; i < PATTERN; i++) mappings.push(patternStub(i, gen));
  for (let i = 0; i < JSONBODY; i++) mappings.push(jsonStub(i, gen));
  return mappings;
}

function importSet(gen, params) {
  return http.post(
    `${ADMIN}/__admin/mappings/import`,
    JSON.stringify({ mappings: stubSet(gen) }),
    params
  );
}

function gauge(text, line) {
  const m = line.exec(text);
  return m === null ? null : Number(m[1]);
}

export function setup() {
  // Below ten stubs the 70/20/10 split rounds a kind away entirely, and the
  // traffic for it would then index into an empty range and spend the run
  // asking for URLs nobody registered.
  if (EXACT < 1 || PATTERN < 1 || JSONBODY < 1) {
    throw new Error(`STUBS=${STUBS} cannot carry S2's 70/20/10 mix: the minimum is 10`);
  }

  const imported = importSet(0, JSON_HEADERS);
  if (imported.status !== 200) {
    throw new Error(`could not import the S8 stub set: ${imported.status} ${imported.body}`);
  }

  // One request of each kind, so a stub set that does not serve fails here
  // instead of arriving as a wall of check failures ten seconds later.
  const warm = [
    http.get(`${BASE}/load/s8/exact/0`),
    http.get(`${BASE}/load/s8/re/0/${hex8(0)}`),
    http.post(`${BASE}/load/s8/json/0`, JSON.stringify({ tenant: 't0' }), JSON_HEADERS),
  ];
  for (const res of warm) {
    if (res.status !== 200 || res.body.length !== BODY) {
      throw new Error(`S8 stub does not serve: ${res.request.url} answered ${res.status} ${res.body}`);
    }
  }

  // The measurement, not the load, is what this scenario exists for, so a rig
  // that cannot produce it has to say so before the run rather than report a
  // ceiling nothing ever tested.
  const metrics = http.get(`${OPS}/metrics`);
  if (metrics.status !== 200) {
    throw new Error(`no /metrics on the ops port ${OPS}: ${metrics.status}`);
  }
  if (gauge(metrics.body, RSS_LINE) === null) {
    throw new Error(
      `${OPS}/metrics exposes no process_resident_memory_bytes, so RSS cannot be ` +
        `read: the process collector reports it on Linux only, and metrics_enabled ` +
        `must be on. Point OPS at the ops port of a Linux instance.`
    );
  }
}

export function load() {
  const n = exec.scenario.iterationInTest;
  const slot = n % 10;
  const cycle = Math.floor(n / 10);

  // Seven exact, two regex, one JSONPath per ten iterations, each kind walking
  // its own range so every stub in the set is requested rather than a subset of
  // it. A mix that only ever touched some of the 1,000 stubs would leave the
  // rest of their bodies cold, and cold bodies are the ones this scenario is
  // counting.
  let res;
  if (slot < 7) {
    const i = (cycle * 7 + slot) % EXACT;
    res = http.get(`${BASE}/load/s8/exact/${i}`, LOAD_GET);
  } else if (slot < 9) {
    const i = (cycle * 2 + slot - 7) % PATTERN;
    res = http.get(`${BASE}/load/s8/re/${i}/${hex8(i)}`, LOAD_GET);
  } else {
    const i = cycle % JSONBODY;
    res = http.post(
      `${BASE}/load/s8/json/${i}`,
      `{"tenant":"t${i}","seq":${n}}`,
      LOAD_POST
    );
  }

  check(res, {
    'status is 200': (r) => r.status === 200,
    'body is intact': (r) => r.body.length === BODY,
  });
}

export function sampleMemory() {
  const res = http.get(`${OPS}/metrics`, SAMPLE_PARAMS);
  if (res.status !== 200) {
    return;
  }

  const rss = gauge(res.body, RSS_LINE);
  if (!check(res, { 'resident memory is exposed': () => rss !== null })) {
    return;
  }
  rssMiB.add(rss / MIB);
  rssSamples.add(1);

  // Recorded beside RSS without a threshold on it. When the ceiling is missed,
  // the pair is what separates "the snapshot got bigger" from "the runtime has
  // not handed pages back yet", and those two have entirely different fixes.
  const heap = gauge(res.body, HEAP_LINE);
  if (heap !== null) {
    heapMiB.add(heap / MIB);
  }
}

// A full re-import of the set, which is the reload S7 measures: the import
// handler bumps the epoch and rebuilds the snapshot whole, rather than splicing
// one stub the way a single create does.
//
// The generation advances by default so every document's content hash changes
// and the compile cache of SPEC §6.2 can reuse nothing. That is the reload that
// tests this ceiling, because for a moment the old snapshot is still serving
// while the new one is fully built beside it, and the transient is proportional
// to the whole set. RELOAD_MODE=warm re-imports byte-identical documents, which
// is the other end of that proportionality and worth a run of its own when a
// number needs explaining.
export function reloadSnapshot() {
  const gen = RELOAD_MODE === 'warm' ? 0 : exec.scenario.iterationInTest + 1;
  const res = importSet(gen, RELOAD_PARAMS);
  if (check(res, { 'snapshot reloaded': (r) => r.status === 200 })) {
    reloadsDone.add(1);
  }
}

// Cleanup goes through remove-by-metadata rather than a global reset: the reset
// endpoints clear the whole deployment, and every other scenario's stubs with
// it (SPEC §1). The tag is the same one every S8 document carries, so this
// removes exactly this scenario's set whatever generation it ended on.
export function teardown() {
  const res = http.post(
    `${ADMIN}/__admin/mappings/remove-by-metadata`,
    JSON.stringify({ equalToJson: JSON.stringify(METADATA) }),
    JSON_HEADERS
  );
  if (res.status !== 200) {
    throw new Error(`S8 stubs were left behind: ${res.status} ${res.body}`);
  }
}
