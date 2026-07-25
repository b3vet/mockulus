// SPDX-License-Identifier: Apache-2.0
//
// S1 — the baseline scenario of SPEC §16.1: one stub, exact URL match, GET.
// Target on the reference rig (1 pod, 2 vCPU, 512Mi): >= 50k RPS sustained,
// p99 < 2 ms at 20k RPS, with a 256 B response body.
//
// This is the perf budget everything else spends, so it is the first thing
// that exists and the number every later milestone is compared against.
//
//   k6 run test/load/s1.js                       # ramp to the throughput target
//   k6 run -e MODE=latency test/load/s1.js       # hold 20k RPS, measure p99
//   k6 run -e BASE=http://host:8080 test/load/s1.js

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || BASE;
const MODE = __ENV.MODE || 'throughput';

// The stub under test. A 256 B body is what the reference rig specifies.
const STUB_PATH = '/load/s1/resource';
const BODY_256 = 'x'.repeat(256);

export const options = MODE === 'latency'
  ? {
      // Latency mode holds the SLO's measurement point rather than pushing
      // for maximum throughput.
      scenarios: {
        s1_latency: {
          executor: 'constant-arrival-rate',
          rate: Number(__ENV.RATE || 20000),
          timeUnit: '1s',
          duration: __ENV.DURATION || '60s',
          preAllocatedVUs: 200,
          maxVUs: 2000,
        },
      },
      // The SLO ties p99 < 2 ms to the 20k measurement point; above it the
      // criterion is sustained throughput with no errors, so the latency
      // threshold only applies at or below that rate.
      thresholds: {
        'http_req_failed': ['rate==0'],
        ...(Number(__ENV.RATE || 20000) <= 20000
          ? { 'http_req_duration': ['p(99)<2'] }
          : {}),
      },
    }
  : {
      scenarios: {
        s1_throughput: {
          executor: 'ramping-arrival-rate',
          startRate: 5000,
          timeUnit: '1s',
          preAllocatedVUs: 200,
          maxVUs: 4000,
          stages: [
            { target: 20000, duration: '30s' },
            { target: 50000, duration: '60s' },
            { target: 50000, duration: '60s' },
          ],
        },
      },
      thresholds: {
        'http_req_failed': ['rate==0'],
        'http_req_duration': ['p(99)<10'],
      },
    };

// setup registers the stub through the public admin API — the harness uses no
// private hooks, exactly as the E2E gate does not (SPEC §19.1).
export function setup() {
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
    throw new Error(`could not register the S1 stub: ${res.status} ${res.body}`);
  }

  const warm = http.get(`${BASE}${STUB_PATH}`);
  if (warm.status !== 200) {
    throw new Error(`S1 stub does not serve: ${warm.status} ${warm.body}`);
  }
  return { id: JSON.parse(res.body).id };
}

export default function () {
  const res = http.get(`${BASE}${STUB_PATH}`);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'body is intact': (r) => r.body.length === 256,
  });
}

export function teardown(data) {
  if (data && data.id) {
    http.del(`${ADMIN}/__admin/mappings/${data.id}`);
  }
  exec.test.options;
}
