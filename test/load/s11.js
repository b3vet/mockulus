// SPDX-License-Identifier: Apache-2.0
//
// S11 — S1's shape with tracing enabled (SPEC §14.3).
//
// This scenario is **informational and non-gating**, and that is the point of
// it existing separately rather than as a mode of s1.js. The SLOs of §16.1 are
// stated for the default configuration, where tracing is off and costs one
// atomic load; a deployment that turns it on is choosing a different trade, and
// what that trade costs should be a measured number in the documentation rather
// than an assurance nobody has checked.
//
// So there is no throughput or latency threshold here. The only thresholds are
// the ones that would mean the run did not measure what it claims to: requests
// must still all succeed, and the collector must actually have been exporting.
// Compare the output against the S1 run from the same night — the difference
// between them is the answer this scenario exists to produce.
//
//   k6 run test/load/s11.js                        # ramp, as S1 does
//   k6 run -e MODE=latency -e RATE=20000 test/load/s11.js
//
// It needs an OTLP/HTTP collector; the driver script starts one that discards
// everything, so the number is mockulus' own cost rather than a measurement of
// somebody's trace backend.

import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE || 'http://localhost:8080';
const ADMIN = __ENV.ADMIN || BASE;
const MODE = __ENV.MODE || 'throughput';

const STUB_PATH = '/load/s11/resource';
const BODY_256 = 'x'.repeat(256);

// Half the requests arrive carrying W3C trace context, because that is the
// shape real traffic has: a mock inside a traced test suite is reached by
// callers that have already decided the trace is being recorded, and those are
// the requests that make mockulus do the most work. Sampling here is
// parent-based, so this fraction — not `tracing.sample_ratio` — is what decides
// how much of the load is actually recorded.
const TRACED_FRACTION = Number(__ENV.TRACED_FRACTION || 0.5);

export const options = MODE === 'latency'
  ? {
      scenarios: {
        s11_latency: {
          executor: 'constant-arrival-rate',
          rate: Number(__ENV.RATE || 20000),
          timeUnit: '1s',
          duration: __ENV.DURATION || '60s',
          preAllocatedVUs: 200,
          maxVUs: 2000,
        },
      },
      // No latency threshold: this run reports a number, it does not judge one.
      thresholds: { http_req_failed: ['rate==0'] },
    }
  : {
      scenarios: {
        s11_throughput: {
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
      thresholds: { http_req_failed: ['rate==0'] },
    };

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
    throw new Error(`could not register the S11 stub: ${res.status} ${res.body}`);
  }

  // A run against an instance that is not exporting would report S1's numbers
  // under S11's name, which is worse than no measurement at all.
  const metrics = http.get(`${ADMIN.replace(/:\d+$/, ':9090')}/metrics`);
  if (metrics.status === 200 && !metrics.body.includes('mockulus_trace_export_failures_total')) {
    throw new Error('this instance exposes no tracing metric; S11 needs tracing enabled');
  }

  http.get(`${BASE}${STUB_PATH}`);
  return {};
}

// hex builds a random id of the given byte length, which is what a traceparent
// carries. k6 has no crypto helper, and these ids need to be distinct rather
// than unguessable.
function hex(bytes) {
  let out = '';
  for (let i = 0; i < bytes * 2; i++) {
    out += '0123456789abcdef'[Math.floor(Math.random() * 16)];
  }
  return out;
}

export default function () {
  const params =
    Math.random() < TRACED_FRACTION
      ? { headers: { traceparent: `00-${hex(16)}-${hex(8)}-01` } }
      : undefined;

  const res = http.get(`${BASE}${STUB_PATH}`, params);
  check(res, { 'status 200': (r) => r.status === 200 });
}
