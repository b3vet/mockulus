// SPDX-License-Identifier: Apache-2.0
//
// S7 — the snapshot reload scenario of SPEC §16.1: a full rebuild at 10k stubs
// while S1-shape load runs against the same pod. Target on the reference rig
// (1 pod, 2 vCPU, 512Mi): cold rebuild (empty compile cache) < 2 s, warm
// rebuild < 500 ms, and under +1 ms of p99 impact on the load stream while the
// rebuild runs.
//
// A reload is triggered by a stub write. The write persists the document, bumps
// the epoch and the pod that handled it rebuilds its snapshot from store state
// before the write returns (SPEC §4.3, §8), so this scenario drives reloads
// through the admin API and reads their cost off /metrics — there is no test
// hook and nothing here that a client could not do. One pod is all it needs:
// the pod being written to is the pod being read from, and its own rebuild is
// the subject. What a second pod would add is propagation, which is S9.
//
// Cold and warm are the same rebuild over the same stub set; what differs is
// how much of it the compile cache of SPEC §6.2 can answer. MODE=warm rewrites
// a single stub, so the rebuild recompiles one document and reuses ten thousand.
// MODE=cold rewrites the whole set, so every content hash moves and not one
// entry is reusable — which is what an empty cache costs, without needing to
// restart the pod to produce one. Restarting is the only other way to empty it,
// and a scenario that has to bounce the process cannot measure a reload that
// happens under load.
//
// The +1 ms is a delta, so both sides of it are measured: a quiet window with
// no writes at all, then a rebuild window with writes driven back to back. Both
// feed one Trend tagged by window, and the thresholds hold the quiet window to
// S1's p99 and the rebuild window to that plus the 1 ms budget.
//
//   k6 run test/load/s7.js                        # warm reload under 20k RPS
//   k6 run -e MODE=cold test/load/s7.js           # cold reload
//   k6 run -e BASE=http://host:8080 -e ADMIN=http://host:9090 test/load/s7.js

import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Trend } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://localhost:8080';

// S1 defaults ADMIN to BASE because `admin_on_mock_port` serves /__admin on the
// mock port too. S7 cannot: the rebuild is measured from /metrics, which lives
// on the admin port and only there (SPEC §14.3), so this scenario needs the
// real admin address whatever the compat setting is.
const ADMIN = __ENV.ADMIN || 'http://localhost:9090';

const MODE = __ENV.MODE || 'warm';
const RATE = Number(__ENV.RATE || 20000);
const STUBS = Number(__ENV.STUBS || 10000);

// Phase lengths, in seconds. The warmup is thrown away — connection setup and
// the arrival-rate ramp are not what a baseline window should contain.
const WARMUP = Number(__ENV.WARMUP || 10);
const QUIET = Number(__ENV.QUIET || 30);
const REBUILD = Number(__ENV.REBUILD || 30);
const SETTLE = Number(__ENV.SETTLE || 5);

// The quiet ceiling is S1's SLO at its measurement point, and the impact budget
// is S7's. They are separate knobs because a rig whose recorded S1 p99 differs
// from the SLO should be compared against its own number: the release criterion
// is the +1 ms, and pinning the quiet side to a stale absolute would let a rig
// that has already regressed on S1 pass or fail S7 for the wrong reason.
const BASELINE_P99 = Number(__ENV.BASELINE_P99 || 2);
const IMPACT = Number(__ENV.IMPACT || 1);

// Zero means fire the next write as soon as the last reload has landed, which
// keeps as much of the rebuild window as possible inside an actual rebuild —
// the SLO is about what a rebuild costs a reader, not about what the average
// second costs one. Set it to `sync_interval` to reproduce the pacing a real
// deployment sees instead, since reloads coalesce to roughly one per interval
// per pod however hard the cluster is being written to (SPEC §8).
const TRIGGER_EVERY = Number(__ENV.TRIGGER_EVERY || 0);

// A 10k import is a multi-megabyte body that the handler answers only after it
// has persisted every document and rebuilt, so it must not be held to k6's
// one-minute default on a store with a real round trip.
const WRITE_TIMEOUT = __ENV.WRITE_TIMEOUT || '300s';

// How long a reload may take to show up before the run is called broken rather
// than slow, and how often to look. Generous against the 2 s target on purpose:
// this is the difference between "missed the SLO" and "never happened", and the
// threshold is what judges the first.
const RELOAD_DEADLINE = 30;
const POLL = 0.025;

const LOAD_PATH = '/load/s7/resource';
const TRIGGER_PATH = '/load/s7/trigger';
const FILL_PATH = '/load/s7/fill/';

// Every stub this scenario registers carries the same metadata, which is what
// lets teardown remove exactly its own and leave the rest of the rig alone. A
// reset or a delete-all would be one call as well, and would take every other
// scenario's stubs with it.
const OWNER = { scenario: 's7' };

const WARMUP_MS = WARMUP * 1000;
const QUIET_TO = WARMUP_MS + QUIET * 1000;
const REBUILD_MS = REBUILD * 1000;
const REBUILD_TO = QUIET_TO + REBUILD_MS;
const RELOAD_BUDGET = MODE === 'cold' ? 2000 : 500;

// Latency of the load stream, tagged by which window the request was issued in.
// One metric rather than two so the summary puts the two numbers the delta is
// computed from next to each other.
const streamLatency = new Trend('stream_latency', true);

// Rebuild time attributable to one triggering write, in milliseconds, read as
// the movement of the reload histogram's sum across the write.
//
// It is a ceiling rather than an exact reading, and deliberately so. The write
// bumps the epoch before the rebuild it starts has swapped, so a poll tick
// landing inside a long rebuild sees the mismatch and queues a second,
// level-triggered reload behind the first (SPEC §8). Both are rebuilds this
// write caused, and the sum counts both, so the number can only overstate what
// the triggered rebuild cost — never flatter it. Attributing the difference by
// dividing over the count would do the opposite: the follow-up reload runs with
// the cache the first one just filled, so averaging a cold rebuild with a warm
// one reports a cold number that no cold rebuild achieved.
const reloadDuration = new Trend('reload_duration', true);

// How many rebuilds each write actually cost, so a reader of the summary can
// see whether the number above carries a follow-up reload or not.
const rebuildsPerWrite = new Trend('rebuilds_per_write');

const thresholds = {
  'http_req_failed': ['rate==0'],
  'reload_duration': [`max<${RELOAD_BUDGET}`],
};

// The p99 pair only means anything at the rate S1's p99 is stated against, the
// same way S1 gates its own p99 at 20k and lets higher rates answer for
// throughput instead. Above it the reload budget still applies — a rebuild does
// not get cheaper because the pod is busier.
if (RATE <= 20000) {
  thresholds['stream_latency{window:quiet}'] = [`p(99)<${BASELINE_P99}`];
  thresholds['stream_latency{window:rebuild}'] = [`p(99)<${BASELINE_P99 + IMPACT}`];
}

export const options = {
  // Registering and removing ten thousand stubs is a single call each, but it
  // is a call the store has to walk.
  setupTimeout: '15m',
  teardownTimeout: '10m',

  // p99 is the number this scenario is about, and the default trend stats stop
  // at p95. Both window rows have to print it or the delta cannot be read off
  // the summary and into BASELINE.md.
  summaryTrendStats: ['min', 'med', 'avg', 'p(95)', 'p(99)', 'max'],

  scenarios: {
    // The load stream is S1's, unchanged: one stub, exact URL match, GET, 256 B
    // body. Holding it to S1's shape is what makes "p99 impact" a statement
    // about the rebuild rather than about the traffic.
    s7_stream: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: `${WARMUP + QUIET + REBUILD + SETTLE}s`,
      preAllocatedVUs: 200,
      maxVUs: 2000,
      exec: 'stream',
    },

    // The writer runs as its own single-VU scenario starting exactly where the
    // quiet window ends. Its start time and the window boundaries are computed
    // from the same constants, which is how the stream VUs know which window
    // they are in without sharing state with it — k6 gives them no way to.
    s7_reload: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
      startTime: `${WARMUP + QUIET}s`,
      maxDuration: `${REBUILD + SETTLE}s`,
      exec: 'driveReloads',
    },
  },

  // No global http_req_duration threshold, unlike S1: the triggering imports
  // are answered in seconds by design and share that metric with the stream, so
  // any ceiling wide enough to admit them would say nothing about the stream.
  thresholds: thresholds,
};

// setup registers the whole stub set in one import so that the snapshot the run
// measures against is the one the SLO names. It goes through the same public
// admin API the E2E gate uses and no other (SPEC §19.1).
//
// Three kinds of stub go in: the one the load stream hits, the one a warm cycle
// rewrites, and the ten thousand a cold cycle rewrites. The served stub is kept
// out of both trigger sets on purpose. It is one document in ten thousand, so
// leaving it cached changes the rebuild's cost by nothing measurable, and it
// keeps the impact number a statement about rebuilding the snapshot rather than
// about recompiling the very stub being read.
export function setup() {
  // A misspelled mode would otherwise run the warm trigger against the warm
  // budget and report a pass for a cold run nobody performed.
  if (MODE !== 'cold' && MODE !== 'warm') {
    throw new Error(`MODE is ${MODE}, want cold or warm`);
  }

  const set = [
    mapping(stubID('0001', 0), LOAD_PATH, padded('s7/load:')),
    mapping(stubID('0002', 0), TRIGGER_PATH, padded('s7/0/trigger:')),
  ].concat(fillers(0));

  const res = importMappings(set);
  if (res.status !== 200) {
    throw new Error(`could not register the S7 stub set: ${res.status} ${res.body}`);
  }

  const warm = http.get(`${BASE}${LOAD_PATH}`);
  if (warm.status !== 200) {
    throw new Error(`S7 load stub does not serve: ${warm.status} ${warm.body}`);
  }

  const seen = scrape();
  if (seen === null) {
    throw new Error(`could not read ${ADMIN}/metrics: the reload is measured from ` +
      'the Prometheus endpoint, which is served on the admin port only, so ADMIN ' +
      'has to name that port rather than the mock one');
  }
  if (seen.stubs < STUBS + 2) {
    throw new Error(`the snapshot holds ${seen.stubs} stubs, want at least ${STUBS + 2}`);
  }
  return { stubs: seen.stubs };
}

// stream is the S1 load, tagged by the window it lands in. Elapsed time is
// measured against this scenario's own start rather than the run's, because the
// run clock also carries setup, and setup here is a ten-thousand-stub import
// whose length is a property of the store rather than of the measurement.
//
// The window is read before the request rather than after it, so a request that
// is slow because a rebuild is running is still counted against the window it
// was issued in.
export function stream() {
  const at = Date.now() - exec.scenario.startTime;
  const res = http.get(`${BASE}${LOAD_PATH}`);

  check(res, {
    'status is 200': (r) => r.status === 200,
    'body is intact': (r) => r.body.length === 256,
  });

  if (at >= WARMUP_MS && at < QUIET_TO) {
    streamLatency.add(res.timings.duration, { window: 'quiet' });
  } else if (at >= QUIET_TO && at < REBUILD_TO) {
    streamLatency.add(res.timings.duration, { window: 'rebuild' });
  }
}

// driveReloads writes stubs for as long as the rebuild window lasts and records
// what each write cost the snapshot. This scenario is scheduled to start where
// the quiet window ends, so its own elapsed time is the position within the
// rebuild window and the two functions never have to agree on a shared clock.
export function driveReloads() {
  let fired = 0;
  let period = 0;

  for (;;) {
    const at = Date.now() - exec.scenario.startTime;
    if (at >= REBUILD_MS) {
      break;
    }
    // Starting a cycle the window has no room left for would put its rebuild in
    // the settle tail, where nothing is measuring the stream — the reload would
    // be timed and its cost to readers would not be.
    if (fired > 0 && at + period > REBUILD_MS) {
      break;
    }

    const startedAt = Date.now();
    const before = scrape();
    if (before === null) {
      exec.test.abort('the admin port stopped answering /metrics mid-run');
    }

    const generation = fired + 1;
    const write = importMappings(MODE === 'cold'
      ? fillers(generation)
      : [mapping(stubID('0002', 0), TRIGGER_PATH, padded(`s7/${generation}/trigger:`))]);
    if (write.status !== 200) {
      exec.test.abort(`the write meant to trigger a reload failed: ${write.status} ${write.body}`);
    }

    const after = awaitReload(before);
    if (after === null) {
      exec.test.abort(`no reload landed within ${RELOAD_DEADLINE}s of the write that triggered it`);
    }
    // A rebuild that dropped stubs is not the rebuild the SLO is about, and its
    // duration would be the wrong number to record.
    if (after.stubs < STUBS + 2) {
      exec.test.abort(`the rebuilt snapshot holds ${after.stubs} stubs, want at least ${STUBS + 2}`);
    }

    reloadDuration.add((after.sum - before.sum) * 1000);
    rebuildsPerWrite.add(after.count - before.count);
    fired++;

    if (TRIGGER_EVERY > 0) {
      const spent = (Date.now() - startedAt) / 1000;
      if (spent < TRIGGER_EVERY) {
        sleep(TRIGGER_EVERY - spent);
      }
    }
    period = Date.now() - startedAt;
  }

  // A run that measured nothing must not report a pass, and an empty Trend
  // satisfies every threshold written over it.
  if (fired === 0) {
    exec.test.abort('the rebuild window ended without a single reload being triggered');
  }
}

// teardown removes exactly the stubs this scenario registered, by the metadata
// every one of them carries. Leaving ten thousand behind would change what
// every other scenario on this rig is measuring.
export function teardown() {
  const res = http.post(`${ADMIN}/__admin/mappings/remove-by-metadata`,
    JSON.stringify({ equalToJson: OWNER }), {
      headers: { 'Content-Type': 'application/json' },
      timeout: WRITE_TIMEOUT,
    });
  if (res.status !== 200) {
    throw new Error('could not remove the S7 stub set, and it is still registered: ' +
      `${res.status} ${res.body}`);
  }
}

// mapping builds one stub document. The id is set rather than left to the
// server because the cold trigger has to overwrite the same documents it
// registered: an import that minted fresh ids every cycle would grow the
// deployment by ten thousand stubs a cycle instead of rewriting it.
function mapping(id, urlPath, body) {
  return {
    id: id,
    metadata: OWNER,
    request: { method: 'GET', urlPath: urlPath },
    response: {
      status: 200,
      body: body,
      headers: { 'Content-Type': 'text/plain' },
    },
  };
}

// fillers is the stub set the reload has to rebuild. They are exact-URL stubs,
// never patterns: a pattern stub is scanned linearly for every request (SPEC
// §6.3), which would make the load stream S2-shaped and the p99 comparison a
// measurement of candidate selection rather than of the rebuild.
//
// The generation is written into every body, so a set imported under a new one
// is byte-different in all ten thousand documents and the compile cache can
// answer none of it. Length stays at 256 B across generations, which keeps the
// memory the rebuild has to allocate the same from cycle to cycle.
function fillers(generation) {
  const out = new Array(STUBS);
  for (let i = 0; i < STUBS; i++) {
    out[i] = mapping(stubID('0003', i), `${FILL_PATH}${i}`, padded(`s7/${generation}/${i}:`));
  }
  return out;
}

function importMappings(mappings) {
  return http.post(`${ADMIN}/__admin/mappings/import`,
    JSON.stringify({ mappings: mappings, importOptions: { duplicatePolicy: 'OVERWRITE' } }), {
      headers: { 'Content-Type': 'application/json' },
      timeout: WRITE_TIMEOUT,
    });
}

// awaitReload waits for the reload histogram to record one more observation
// than it had before the write. In cold mode the rebuild is finished by the
// time the import answers, so the first look already sees it; the loop is what
// covers the warm case and any store slow enough to lag its own write.
function awaitReload(before) {
  const deadline = Date.now() + RELOAD_DEADLINE * 1000;
  for (;;) {
    const now = scrape();
    if (now !== null && now.count > before.count) {
      return now;
    }
    if (Date.now() >= deadline) {
      return null;
    }
    sleep(POLL);
  }
}

// scrape reads the three series this scenario needs out of the Prometheus text
// exposition. Every one of them is unlabelled, so anchoring the name against
// the space that separates it from its value is enough to tell the histogram's
// _sum and _count apart from its buckets.
function scrape() {
  const res = http.get(`${ADMIN}/metrics`);
  if (res.status !== 200) {
    return null;
  }
  const sum = series(res.body, 'mockulus_snapshot_reload_duration_seconds_sum');
  const count = series(res.body, 'mockulus_snapshot_reload_duration_seconds_count');
  const stubs = series(res.body, 'mockulus_snapshot_stubs');
  if (isNaN(sum) || isNaN(count) || isNaN(stubs)) {
    return null;
  }
  return { sum: sum, count: count, stubs: stubs };
}

function series(text, name) {
  const found = new RegExp(`^${name} (\\S+)$`, 'm').exec(text);
  return found === null ? NaN : Number(found[1]);
}

// stubID mints a canonical UUID from an index. A stub id is deserialised as a
// UUID and anything else is refused (SPEC §5.5 deviation 24), so ids that read
// as names are not an option and the index goes into the last group instead.
function stubID(kind, n) {
  return `57000000-0000-4000-8000-${kind}${n.toString(16).padStart(8, '0')}`;
}

// padded fills a label out to the 256 B body the reference rig specifies.
function padded(label) {
  return label + 'x'.repeat(Math.max(0, 256 - label.length));
}
