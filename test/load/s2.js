// SPDX-License-Identifier: Apache-2.0
//
// S2 — the mixed stub set of SPEC §16.1: 1,000 stubs at 70% exact URL, 20%
// regex and 10% JSONPath-body criterion, driven with traffic in the same
// proportion. Target on the reference rig (1 pod, 2 vCPU, 512Mi): >= 30k RPS,
// p99 < 3 ms at 15k RPS, with a 256 B response body.
//
// S1 measures the floor of the serve path with nothing in the way. This
// measures what candidate selection costs once the pattern list of SPEC §6.3
// is no longer empty: every GET is checked against the literal prefix of each
// pattern stub that sorts ahead of its answer, and the body-matching tenth
// pays a pass over the request body on top of that. Those two are the reason
// S2's target is 30k where S1's is 50k, and this script is where that gap
// stops being a prediction.
//
// The three shapes are the ones `mixedSnapshot` builds in internal/match's
// benchmarks, moved under this scenario's URL namespace and nothing else
// changed, so the RPS recorded here and the Match/mixed/1000 rows of
// BASELINE.md describe the same stub set and can be read against each other.
//
// The rig is the zero-config one of compose.yaml: a single instance, memory
// store, journal off, no templating. The store is touched by setup and
// teardown and never on the serve path, so a Couchbase deployment measures the
// same thing here — S9 and S10 are where the store earns its own scenario.
//
//   k6 run test/load/s2.js                        # ramp to the throughput target
//   k6 run -e MODE=latency test/load/s2.js        # hold 15k RPS, measure p99
//   k6 run -e BASE=http://host:8080 test/load/s2.js

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Trend } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || BASE;
const MODE = __ENV.MODE || 'throughput';

// The stub count of the S2 row. Keep it a multiple of ten: the 70/20/10 split
// is cut from the low digit of the stub index, and a count that is not a whole
// number of tens leaves the last cycle short of its regex and body stubs.
const STUBS = Number(__ENV.STUBS || 1000);

// Every stub carries this tag, and teardown removes exactly the stubs that
// carry it. SPEC §1 asks for this over a global reset on any deployment that
// might be shared, and the rig is shared: `POST /__admin/reset` here would
// take the next scenario's stubs with it.
const SUITE = 'load-s2';

// The response is identical on all three shapes, so a shape's cost is its
// matching cost and nothing else. 256 B is what the reference rig specifies.
const BODY_256 = 'x'.repeat(256);
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

// The document the JSONPath tenth is matched against, byte for byte the one
// the matching benchmarks use. `$.card.brand` is a definite path, so the
// criterion is answered by a scan over these bytes rather than by decoding
// them (SPEC §6.7).
const PAYMENT_BODY = JSON.stringify({
  amount: 1299,
  currency: 'EUR',
  card: { brand: 'visa', last4: '4242' },
});

// Successive iterations step this far through the stub set instead of walking
// it in order. A march through adjacent indexes would keep the whole fleet in
// one corner of the URL index at any instant, which is not the access pattern
// a thousand-stub deployment sees. 7919 is prime, so it is coprime with any
// stub count that is not a multiple of it and the sweep still reaches every
// stub; and because it ends in 9, ten consecutive iterations land on ten
// different low digits — the 70/20/10 traffic split therefore holds in every
// window of ten requests rather than only in the total.
const STRIDE = 7919;

// Per-shape latency, recorded and not gated. The SLO is stated over the mix,
// so gating a shape would fail runs that meet the release criterion; but the
// mix is the whole point of S2, and whoever fills in BASELINE.md wants to know
// which third of it is expensive. Each trend's sample count is also the check
// that the traffic split came out at 70/20/10. The default summary reports a
// custom trend at p(95) and no further, so read them with
// --summary-trend-stats=avg,p(95),p(99),max when a baseline is being recorded.
const exactDuration = new Trend('s2_exact_duration', true);
const regexDuration = new Trend('s2_regex_duration', true);
const jsonPathDuration = new Trend('s2_jsonpath_duration', true);

export const options = MODE === 'latency'
  ? {
      // Latency mode holds the SLO's measurement point rather than pushing for
      // maximum throughput.
      scenarios: {
        s2_latency: {
          executor: 'constant-arrival-rate',
          rate: Number(__ENV.RATE || 15000),
          timeUnit: '1s',
          duration: __ENV.DURATION || '60s',
          preAllocatedVUs: 200,
          maxVUs: 2000,
        },
      },
      // The SLO ties p99 < 3 ms to the 15k measurement point; above it the
      // criterion is sustained throughput with no errors, so the latency
      // threshold only applies at or below that rate.
      thresholds: {
        'http_req_failed': ['rate==0'],
        ...(Number(__ENV.RATE || 15000) <= 15000
          ? { 'http_req_duration': ['p(99)<3'] }
          : {}),
      },
    }
  : {
      scenarios: {
        s2_throughput: {
          executor: 'ramping-arrival-rate',
          startRate: 5000,
          timeUnit: '1s',
          preAllocatedVUs: 200,
          maxVUs: 4000,
          stages: [
            { target: 15000, duration: '30s' },
            { target: 30000, duration: '60s' },
            { target: 30000, duration: '60s' },
          ],
        },
      },
      // The ramp deliberately runs to the edge of what the rig will do, so the
      // ceiling here is a collapse detector rather than the SLO — five times
      // the measured target, the same margin S1's ramp allows itself. A 404
      // from a mix that stopped lining up with its stubs shows up as a failed
      // request, which is the failure this really guards against.
      thresholds: {
        'http_req_failed': ['rate==0'],
        'http_req_duration': ['p(99)<15'],
      },
    };

// Index i decides a stub's shape by its low digit: seven exact, two regex, one
// body criterion out of every ten. A request derives its URL from the same
// digit, so a stub and the traffic aimed at it cannot drift apart.
function slot(i) {
  return i % 10;
}

function index(i) {
  return String(i).padStart(6, '0');
}

function exactURL(i) {
  return `/load/s2/exact/${index(i)}/orders`;
}

function regexURL(i) {
  return `/load/s2/regex/${index(i)}/sku-a1b2c3`;
}

function jsonPathURL(i) {
  return `/load/s2/jsonpath/${index(i)}/authorize`;
}

// Ids are derived from the index rather than left to the server. The import
// overwrites a duplicate id in place, so a run started after a teardown that
// never happened replaces its own thousand stubs instead of registering a
// second thousand behind the same URLs. The first group is a fixed marker, so
// a stub found loose in a shared deployment is traceable to this script by its
// id alone.
function stubID(i) {
  return `10ad0002-0000-4000-8000-${i.toString(16).padStart(12, '0')}`;
}

function response() {
  return {
    status: 200,
    body: BODY_256,
    headers: { 'Content-Type': 'text/plain' },
  };
}

function mappingFor(i) {
  const common = { id: stubID(i), metadata: { suite: SUITE }, response: response() };
  const s = slot(i);

  if (s < 7) {
    return { ...common, request: { method: 'GET', urlPath: exactURL(i) } };
  }
  if (s < 9) {
    // The pattern carries a literal prefix, which is what SPEC §6.3 prefilters
    // the linear pattern list on. A prefix-free pattern would measure a regex
    // engine instead of a deployment.
    return {
      ...common,
      request: { method: 'GET', urlPathPattern: `/load/s2/regex/${index(i)}/sku-[a-z0-9]+` },
    };
  }
  // The body criterion needs a request with a body to read, so this tenth is
  // POST. The URL is still exact: the criterion under measurement is the
  // JSONPath, and a pattern URL in front of it would hide what it costs.
  return {
    ...common,
    request: {
      method: 'POST',
      urlPath: jsonPathURL(i),
      bodyPatterns: [{ matchesJsonPath: { expression: '$.card.brand', equalTo: 'visa' } }],
    },
  };
}

// The one cleanup call, by the tag every stub of this run carries.
function removeSuite() {
  return http.post(
    `${ADMIN}/__admin/mappings/remove-by-metadata`,
    JSON.stringify({ equalToJson: JSON.stringify({ suite: SUITE }) }),
    JSON_HEADERS,
  );
}

// Anything that goes wrong from the import onwards has to take the import with
// it. A throw out of setup does not run teardown, and a thousand stubs left on
// the rig would become the next scenario's mystery rather than this one's
// failure. An import that answers anything but 200 counts: the batch is
// validated whole before a single document is written, but a store that fails
// halfway through the writing still leaves some of them behind.
function abort(message) {
  removeSuite();
  throw new Error(message);
}

export function setup() {
  const mappings = [];
  for (let i = 0; i < STUBS; i++) {
    mappings.push(mappingFor(i));
  }

  // One import rather than a thousand creates: each admin write bumps the
  // epoch and rebuilds the snapshot, so registering these one at a time would
  // spend minutes rebuilding and would measure a pod that had just done so.
  // OVERWRITE is the default and is named anyway, because the neighbouring
  // option — deleteAllNotInImport — would delete every stub this scenario does
  // not own, and the difference deserves to be visible at the call site.
  const body = JSON.stringify({
    mappings,
    importOptions: { duplicatePolicy: 'OVERWRITE' },
  });
  const res = http.post(`${ADMIN}/__admin/mappings/import`, body, JSON_HEADERS);
  if (res.status !== 200) {
    abort(`could not import the S2 stub set: ${res.status} ${res.body}`);
  }

  // The import answers 200 with no body, so what actually landed is read back
  // through the same matcher teardown will clean up with. This is the one
  // assertion that proves the tag is on the stubs before a run leans on it to
  // remove them; a mismatch here means either a short import or stubs left by
  // an earlier run under a different count, and both want a person.
  const found = http.post(
    `${ADMIN}/__admin/mappings/find-by-metadata`,
    JSON.stringify({ equalToJson: JSON.stringify({ suite: SUITE }) }),
    JSON_HEADERS,
  );
  if (found.status !== 200) {
    abort(`could not read back the S2 stub set: ${found.status} ${found.body}`);
  }
  const total = JSON.parse(found.body).meta.total;
  if (total !== STUBS) {
    abort(
      `expected ${STUBS} stubs tagged ${SUITE}, found ${total}; ` +
        'clear them with POST /__admin/mappings/remove-by-metadata and run again',
    );
  }

  // One request of each shape before the load starts, so a mix that does not
  // line up with its stubs fails here with a legible message rather than as a
  // wall of 404s inside a threshold.
  const warm = [
    { name: 'exact', res: http.get(`${BASE}${exactURL(0)}`) },
    { name: 'regex', res: http.get(`${BASE}${regexURL(7)}`) },
    { name: 'jsonpath', res: http.post(`${BASE}${jsonPathURL(9)}`, PAYMENT_BODY, JSON_HEADERS) },
  ];
  for (const probe of warm) {
    if (probe.res.status !== 200 || probe.res.body.length !== 256) {
      abort(`S2 ${probe.name} stub does not serve: ${probe.res.status} ${probe.res.body}`);
    }
  }

  return { stubs: STUBS };
}

export default function () {
  // Which stub an iteration hits is a pure function of the global iteration
  // counter, so two runs drive the same requests in the same proportions and
  // their numbers can be compared without a sampling argument in the way.
  const i = (exec.scenario.iterationInTest * STRIDE) % STUBS;
  const s = slot(i);

  // Import order is registration order, and selection order is its reverse
  // (SPEC §6.1, seq descending), so an early stub sits behind nearly every
  // pattern stub in the scan and a late one behind almost none. Sweeping the
  // whole set is what makes the recorded p99 the mix's average scan depth
  // rather than whichever depth a single fixed URL happened to sit at.
  let res;
  if (s < 7) {
    res = http.get(`${BASE}${exactURL(i)}`);
    exactDuration.add(res.timings.duration);
  } else if (s < 9) {
    res = http.get(`${BASE}${regexURL(i)}`);
    regexDuration.add(res.timings.duration);
  } else {
    res = http.post(`${BASE}${jsonPathURL(i)}`, PAYMENT_BODY, JSON_HEADERS);
    jsonPathDuration.add(res.timings.duration);
  }

  check(res, {
    'status is 200': (r) => r.status === 200,
    'body is intact': (r) => r.body.length === 256,
  });
}

// Cleanup keys off the constant the stubs were tagged with rather than off
// anything setup handed back, so it does not depend on how far setup got.
export function teardown() {
  const res = removeSuite();
  if (res.status !== 200) {
    throw new Error(`could not remove the S2 stub set: ${res.status} ${res.body}`);
  }
  const removed = JSON.parse(res.body).meta.total;
  if (removed !== STUBS) {
    throw new Error(
      `removed ${removed} of ${STUBS} S2 stubs; the rig is shared, so check ` +
        'POST /__admin/mappings/find-by-metadata before running anything else',
    );
  }
}
