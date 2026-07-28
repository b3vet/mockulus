// SPDX-License-Identifier: Apache-2.0
//
// S3 — the templating scenario of SPEC §16.1: S2's stub set with a response
// template on every stub. Target on the reference rig (1 pod, 2 vCPU, 512Mi):
// >= 20k RPS, p99 < 5 ms, with a 256 B response body.
//
// S2 measures what it costs to find the right stub among a thousand. This
// measures what it costs to build the answer once it has been found, so the
// stub set, the URLs, the criteria and the traffic pattern are S2's and the
// only thing that differs is the response: 1,000 templates that read this
// request's body through jsonPath and the clock through now. S3 minus S2 is
// the render, which is the reason both scenarios exist.
//
// The subtraction is honest about one thing. The 90% of requests S2 drives
// with no body at all carry one here, because a template that reads the
// request needs a request to read, so the difference also contains the cost of
// taking 71 further bytes off the socket. That term is small and it is not
// removable without giving up the helper the target names.
//
// The stub set below is a deliberate copy of s2.js rather than an import from
// it. These files are the record of what was measured: a shared builder would
// let a change made for S2 move S3's number, and a stored baseline nobody
// edited would quietly stop describing the run that produced it. The rule that
// replaces the import is that the two shapes change together — when S2's stub
// set moves, this one moves with it and both baselines are re-recorded.
//
// The rig is the zero-config one of compose.yaml. Templating is opted into per
// stub here, so the default `templating_enabled: wm-compat` serves these
// without configuration, and the store is touched by setup and teardown only.
//
//   k6 run test/load/s3.js                        # ramp to the throughput target
//   k6 run -e MODE=latency test/load/s3.js        # hold 20k RPS, measure p99
//   k6 run -e BASE=http://host:8080 test/load/s3.js

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Trend } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || BASE;
const MODE = __ENV.MODE || 'throughput';

// The stub count of the S3 row. Keep it a multiple of ten: the 70/20/10 split
// is cut from the low digit of the stub index, and a count that is not a whole
// number of tens leaves the last cycle short of its regex and body stubs.
const STUBS = Number(__ENV.STUBS || 1000);

// Every stub carries this tag, and teardown removes exactly the stubs that
// carry it. SPEC §1 asks for this over a global reset on any deployment that
// might be shared, and the rig is shared: `POST /__admin/reset` here would take
// the next scenario's stubs with it.
const SUITE = 'load-s3';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

// The document every request carries, byte for byte S2's. Two of its fields
// are what the templates read back, and `$.card.brand` is also the criterion
// the JSONPath tenth matches on — a stub that renders what it matched on is
// the shape the spec's own example stub has, and it is the case worth
// measuring, because the path is walked twice over the same bytes with nothing
// shared between the matcher and the renderer.
const PAYMENT_BODY = JSON.stringify({
  amount: 1299,
  currency: 'EUR',
  card: { brand: 'visa', last4: '4242' },
});
const CARD_BRAND = 'visa';

// Both helpers the target names, each reading something the request or the
// clock supplies. A template that interpolated a constant would measure a copy
// instead of a render — and a body with no `{{` in it never reaches the engine
// at all (SPEC §10.1), which is precisely the S2 case this scenario is the
// counterpart to.
const BRAND_EXPR = "{{jsonPath request.body '$.card.brand'}}";
const LAST4_EXPR = "{{jsonPath request.body '$.card.last4'}}";
const NOW_EXPR = "{{now format='yyyy-MM-dd HH:mm:ss'}}";

// What those three render to is fixed width, which is what lets the pad below
// land every response on the 256 B the reference rig specifies and S2 serves.
// Left to vary, the response size would drift and the two scenarios would no
// longer be measuring the same amount of writing.
const BRAND_WIDTH = 4; //  visa
const LAST4_WIDTH = 4; //  4242
const NOW_WIDTH = 19; //   2026-07-28 12:34:56
const INDEX_WIDTH = 6; //  000699
const RESPONSE_BYTES = 256;

function templateBody(tag, pad) {
  return `{"stub":"${tag}","brand":"${BRAND_EXPR}","last4":"${LAST4_EXPR}",` +
    `"at":"${NOW_EXPR}","pad":"${pad}"}`;
}

const RENDER_OVERHEAD = templateBody('0'.repeat(INDEX_WIDTH), '').length -
  (BRAND_EXPR.length - BRAND_WIDTH) -
  (LAST4_EXPR.length - LAST4_WIDTH) -
  (NOW_EXPR.length - NOW_WIDTH);
const PAD = 'x'.repeat(RESPONSE_BYTES - RENDER_OVERHEAD);

// Successive iterations step this far through the stub set instead of walking
// it in order, for the reason s2.js gives: a march through adjacent indexes
// keeps the whole fleet in one corner of the URL index at any instant, which
// is not the access pattern a thousand-stub deployment sees. 7919 is prime, so
// the sweep reaches every stub, and because it ends in 9 the 70/20/10 split
// holds in every window of ten requests rather than only in the total.
const STRIDE = 7919;

// Per-shape latency, recorded and not gated. The SLO is stated over the mix,
// so gating a shape would fail runs that meet the release criterion; but which
// shape carries the render cost is the question S3 exists to answer, and these
// are what answer it. The default summary reports a custom trend at p(95) and
// no further, so read them with
// --summary-trend-stats=avg,p(95),p(99),max when a baseline is being recorded.
const exactDuration = new Trend('s3_exact_duration', true);
const regexDuration = new Trend('s3_regex_duration', true);
const jsonPathDuration = new Trend('s3_jsonpath_duration', true);

export const options = MODE === 'latency'
  ? {
      // Latency mode holds the SLO's measurement point rather than pushing for
      // maximum throughput.
      scenarios: {
        s3_latency: {
          executor: 'constant-arrival-rate',
          rate: Number(__ENV.RATE || 20000),
          timeUnit: '1s',
          duration: __ENV.DURATION || '60s',
          preAllocatedVUs: 300,
          maxVUs: 2000,
        },
      },
      // S3's row states its p99 without naming a separate measurement point
      // the way S1's and S2's do, so the point is the throughput target
      // itself: p99 < 5 ms at 20k. Above that the run is exploring headroom
      // rather than testing the release criterion, and the latency threshold
      // drops away exactly as it does in S1 and S2.
      //
      // The checks are a threshold here rather than a report, which is the one
      // way S3 has to depart from its neighbours. They are the only thing that
      // can tell a rendered response from an unrendered one: a deployment with
      // templating off, or a serve-time render error — which SPEC §10.4 writes
      // into the body and still answers 200 with — would post an excellent p99
      // for work the pod never did, and http_req_failed cannot see either.
      thresholds: {
        'http_req_failed': ['rate==0'],
        'checks': ['rate==1'],
        ...(Number(__ENV.RATE || 20000) <= 20000
          ? { 'http_req_duration': ['p(99)<5'] }
          : {}),
      },
    }
  : {
      scenarios: {
        s3_throughput: {
          executor: 'ramping-arrival-rate',
          startRate: 5000,
          timeUnit: '1s',
          preAllocatedVUs: 300,
          maxVUs: 4000,
          stages: [
            { target: 10000, duration: '30s' },
            { target: 20000, duration: '60s' },
            { target: 20000, duration: '60s' },
          ],
        },
      },
      // Five times the target, the same margin S1 and S2 allow their ramps: a
      // collapse detector rather than the SLO, since the number that decides
      // p99 comes from latency mode holding the rate flat. What it catches is a
      // run that keeps its arrival rate only by queueing, which reads as
      // throughput met right up until the latency is looked at.
      thresholds: {
        'http_req_failed': ['rate==0'],
        'checks': ['rate==1'],
        'http_req_duration': ['p(99)<25'],
      },
    };

// Index i decides a stub's shape by its low digit: seven exact, two regex, one
// body criterion out of every ten. A request derives its URL from the same
// digit, so a stub and the traffic aimed at it cannot drift apart.
function slot(i) {
  return i % 10;
}

function index(i) {
  return String(i).padStart(INDEX_WIDTH, '0');
}

function exactURL(i) {
  return `/load/s3/exact/${index(i)}/orders`;
}

function regexURL(i) {
  return `/load/s3/regex/${index(i)}/sku-a1b2c3`;
}

function jsonPathURL(i) {
  return `/load/s3/jsonpath/${index(i)}/authorize`;
}

// Ids are derived from the index rather than left to the server. The import
// overwrites a duplicate id in place, so a run started after a teardown that
// never happened replaces its own thousand stubs instead of registering a
// second thousand behind the same URLs. The first group is a fixed marker, so
// a stub found loose in a shared deployment is traceable to this script by its
// id alone.
function stubID(i) {
  return `10ad0003-0000-4000-8000-${i.toString(16).padStart(12, '0')}`;
}

// The response is the same template on all three shapes, so a shape's cost is
// its matching cost plus one constant render and nothing else.
function response(i) {
  return {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    transformers: ['response-template'],
    body: templateBody(index(i), PAD),
  };
}

function mappingFor(i) {
  const common = { id: stubID(i), metadata: { suite: SUITE }, response: response(i) };
  const s = slot(i);

  // Every shape is POST where S2 has GET on the first two, because the
  // template has to have a body to read. Nothing about the matching moves with
  // it: the method is part of the exact-URL index key either way, and the
  // pattern list is prefiltered by method either way, so the body is read and
  // then ignored by the matcher on these two shapes (SPEC §6.4).
  if (s < 7) {
    return { ...common, request: { method: 'POST', urlPath: exactURL(i) } };
  }
  if (s < 9) {
    // The pattern carries a literal prefix, which is what SPEC §6.3 prefilters
    // the linear pattern list on. A prefix-free pattern would measure a regex
    // engine instead of a deployment.
    return {
      ...common,
      request: { method: 'POST', urlPathPattern: `/load/s3/regex/${index(i)}/sku-[a-z0-9]+` },
    };
  }
  // The URL of the body-criterion tenth is still exact: what is under
  // measurement is the JSONPath, and a pattern URL in front of it would hide
  // what it costs.
  return {
    ...common,
    request: {
      method: 'POST',
      urlPath: jsonPathURL(i),
      bodyPatterns: [{ matchesJsonPath: { expression: '$.card.brand', equalTo: CARD_BRAND } }],
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
    abort(`could not import the S3 stub set: ${res.status} ${res.body}`);
  }

  // That 200 carries one fact this scenario needs on its own: templates are
  // compiled at registration (SPEC §10.1), so an import that succeeded is a
  // thousand templates that parsed and stayed inside the helper allowlist.
  //
  // It says nothing about how many landed, though, and the import answers with
  // no body, so what did is read back through the same matcher teardown will
  // clean up with.
  const found = http.post(
    `${ADMIN}/__admin/mappings/find-by-metadata`,
    JSON.stringify({ equalToJson: JSON.stringify({ suite: SUITE }) }),
    JSON_HEADERS,
  );
  if (found.status !== 200) {
    abort(`could not read back the S3 stub set: ${found.status} ${found.body}`);
  }
  const total = JSON.parse(found.body).meta.total;
  if (total !== STUBS) {
    abort(
      `expected ${STUBS} stubs tagged ${SUITE}, found ${total}; ` +
        'setup sweeps the tag on its way out of this, so run again and read ' +
        'POST /__admin/mappings/find-by-metadata by hand if it happens twice',
    );
  }

  // One request of each shape before the load starts, so a mix that does not
  // line up with its stubs fails here with a legible message rather than as a
  // wall of 404s inside a threshold. What each probe is really checking is
  // that the response was rendered: an unrendered body still answers 200, and
  // a run that never noticed would report the throughput of a memcpy.
  let bodyBytes = 0;
  for (const probe of [
    { name: 'exact', url: exactURL(0) },
    { name: 'regex', url: regexURL(7) },
    { name: 'jsonpath', url: jsonPathURL(9) },
  ]) {
    const warm = http.post(`${BASE}${probe.url}`, PAYMENT_BODY, JSON_HEADERS);
    if (warm.status !== 200) {
      abort(`S3 ${probe.name} stub does not serve: ${warm.status} ${warm.body}`);
    }
    if (warm.body.indexOf('{{') !== -1) {
      abort(`S3 ${probe.name} response was not rendered: ${warm.body}`);
    }
    if (warm.body.indexOf(CARD_BRAND) === -1) {
      abort(`S3 ${probe.name} template did not read the request body: ${warm.body}`);
    }
    if (bodyBytes && warm.body.length !== bodyBytes) {
      abort(`S3 shapes render to different sizes: ${warm.body.length} at ${probe.url}, ${bodyBytes} before`);
    }
    bodyBytes = warm.body.length;
  }

  // The response size is part of the rig rather than an incidental: a body
  // that is not 256 B is not comparable with S1, S2, or a stored S3 baseline.
  // If a helper's output width ever changes this is where it is noticed, and
  // the fix is the width constants above rather than a footnote on the number.
  if (bodyBytes !== RESPONSE_BYTES) {
    abort(`S3 renders ${bodyBytes} B, not ${RESPONSE_BYTES} B; adjust the width constants`);
  }

  return { bodyBytes };
}

export default function (data) {
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
    res = http.post(`${BASE}${exactURL(i)}`, PAYMENT_BODY, JSON_HEADERS);
    exactDuration.add(res.timings.duration);
  } else if (s < 9) {
    res = http.post(`${BASE}${regexURL(i)}`, PAYMENT_BODY, JSON_HEADERS);
    regexDuration.add(res.timings.duration);
  } else {
    res = http.post(`${BASE}${jsonPathURL(i)}`, PAYMENT_BODY, JSON_HEADERS);
    jsonPathDuration.add(res.timings.duration);
  }

  check(res, {
    'status is 200': (r) => r.status === 200,
    'body is intact': (r) => r.body.length === data.bodyBytes,
    // The pad is a run of x, so the card brand can only be in this response
    // because the template put it there, having read it out of the request.
    'response was rendered': (r) => r.body.indexOf(CARD_BRAND) !== -1,
  });
}

// Cleanup keys off the constant the stubs were tagged with rather than off
// anything setup handed back, so it does not depend on how far setup got.
export function teardown() {
  const res = removeSuite();
  if (res.status !== 200) {
    throw new Error(`could not remove the S3 stub set: ${res.status} ${res.body}`);
  }
  const removed = JSON.parse(res.body).meta.total;
  if (removed !== STUBS) {
    throw new Error(
      `removed ${removed} of ${STUBS} S3 stubs; the rig is shared, so check ` +
        'POST /__admin/mappings/find-by-metadata before running anything else',
    );
  }
}
