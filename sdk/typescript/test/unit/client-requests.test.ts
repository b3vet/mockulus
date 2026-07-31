// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { MockulusClient } from '../../src/index.js';
import { bodyless, json, stubFetch, type RecordedCall } from './stub-fetch.js';

/**
 * What the client puts on the wire.
 *
 * These are the assertions that no amount of integration testing makes: a
 * misspelled query parameter, a parameter sent on an endpoint that does not
 * read one, or a token under the wrong scheme all produce a perfectly ordinary
 * 200 from a server that quietly ignored them. The cases below pin the request
 * itself, which is the only place that mistake is visible.
 */

/** A client whose every call is answered `200 {}`, with the log of the calls. */
function client(options: { baseUrl?: string; token?: string; headers?: Record<string, string> }) {
  const stub = stubFetch(() => json(200, {}));
  return {
    calls: stub.calls,
    client: new MockulusClient({
      baseUrl: options.baseUrl ?? 'http://mock.test:9090',
      fetch: stub.fetch,
      ...(options.token === undefined ? {} : { token: options.token }),
      ...(options.headers === undefined ? {} : { headers: options.headers }),
    }),
  };
}

/** The one call a case expected, failing rather than answering `undefined`. */
function only(recorded: RecordedCall[]): RecordedCall {
  const call = recorded[0];
  if (!call) throw new Error('no call was recorded');
  return call;
}

/** The path and query of one recorded call, which is what most cases assert on. */
function target(call: RecordedCall | undefined): string {
  if (!call) throw new Error('no call was recorded');
  return call.url.pathname + call.url.search;
}

describe('the URLs the client builds', () => {
  it('joins a base URL that carries a trailing slash', async () => {
    const { client: c, calls } = client({ baseUrl: 'http://mock.test:9090/' });
    await c.system.health();
    expect(calls[0]?.url.toString()).toBe('http://mock.test:9090/__admin/health');
  });

  it('keeps a path prefix, for a deployment behind an ingress', async () => {
    const { client: c, calls } = client({ baseUrl: 'http://gateway.test/mockulus' });
    await c.system.version();
    expect(calls[0]?.url.toString()).toBe('http://gateway.test/mockulus/__admin/version');
  });

  it('sends limit and offset on the mappings listing', async () => {
    const { client: c, calls } = client({});
    await c.mappings.list({ limit: 25, offset: 50 });
    expect(target(calls[0])).toBe('/__admin/mappings?limit=25&offset=50');
  });

  it('sends no query at all when the listing is unbounded', async () => {
    const { client: c, calls } = client({});
    await c.mappings.list();
    // Not `?limit=&offset=`: an empty value is a value, and the server reads a
    // parameter it cannot parse as absent only by luck rather than by contract.
    expect(target(calls[0])).toBe('/__admin/mappings');
  });

  it('sends limit on the journal listing and never an offset', async () => {
    const { client: c, calls } = client({});
    await c.requests.list({ limit: 1 });
    // `limit` exists on this endpoint alone and `offset` exists nowhere under
    // `/__admin/requests`. The client has no way to send one, and this is the
    // case that keeps it that way.
    expect(target(calls[0])).toBe('/__admin/requests?limit=1');
    expect(calls[0]?.url.searchParams.has('offset')).toBe(false);
  });

  it('escapes a path segment rather than letting it change the route', async () => {
    const { client: c, calls } = client({});
    await c.scenarios.setState('order flow?x=1', 'Started');
    expect(target(calls[0])).toBe('/__admin/scenarios/order%20flow%3Fx%3D1/state');
  });

  it('keeps the slashes in a file name, which is one name and not a route', async () => {
    const { client: c, calls } = client({});
    await c.files.delete('fixtures/large body.bin');
    expect(target(calls[0])).toBe('/__admin/files/fixtures/large%20body.bin');
  });
});

describe('the `since` window', () => {
  it('is offered on all seven endpoints that honour one', async () => {
    const { client: c, calls } = client({});
    const since = '2026-07-29T10:15:00Z';

    // Every journal-backed endpoint reads `since`, including the three that
    // carry their criteria in a body — which is where a client most wants it.
    // A client that offered it only on the plain listing would leave a windowed
    // verification no way to say "since the test started".
    await c.requests.list({ since });
    await c.requests.unmatched({ since });
    await c.requests.unmatchedNearMisses({ since });
    await c.requests.count({}, { since });
    await c.requests.find({}, { since });
    await c.requests.remove({}, { since });
    await c.nearMisses.forRequestPattern({}, { since });

    expect(calls.map(target)).toEqual([
      `/__admin/requests?since=${encodeURIComponent(since)}`,
      `/__admin/requests/unmatched?since=${encodeURIComponent(since)}`,
      `/__admin/requests/unmatched/near-misses?since=${encodeURIComponent(since)}`,
      `/__admin/requests/count?since=${encodeURIComponent(since)}`,
      `/__admin/requests/find?since=${encodeURIComponent(since)}`,
      `/__admin/requests/remove?since=${encodeURIComponent(since)}`,
      `/__admin/near-misses/request-pattern?since=${encodeURIComponent(since)}`,
    ]);
  });

  it('renders a Date as the RFC 3339 the server parses', async () => {
    const { client: c, calls } = client({});
    await c.requests.list({ since: new Date(Date.UTC(2026, 6, 29, 10, 15, 0)) });
    // Not `String(date)`, which is `Wed Jul 29 2026 …` and is refused with a
    // 422 rather than dropped — the refusal being the whole point of `since`.
    expect(calls[0]?.url.searchParams.get('since')).toBe('2026-07-29T10:15:00.000Z');
  });

  it('is absent from the query when the caller did not ask for a window', async () => {
    const { client: c, calls } = client({});
    await c.requests.find({ method: 'GET' });
    expect(target(calls[0])).toBe('/__admin/requests/find');
  });
});

describe('the admin token', () => {
  it('travels under the `Token` scheme, which is WireMock’s and not `Bearer`', async () => {
    const { client: c, calls } = client({ token: 's3cret' });
    await c.mappings.list();
    expect(calls[0]?.headers.get('authorization')).toBe('Token s3cret');
  });

  it('is absent when the deployment has none, which is the default', async () => {
    const { client: c, calls } = client({});
    await c.mappings.list();
    expect(calls[0]?.headers.has('authorization')).toBe(false);
  });

  it('does not stop a caller adding headers of their own', async () => {
    const { client: c, calls } = client({ token: 's3cret', headers: { 'X-Suite': 'checkout' } });
    await c.mappings.list({ headers: { 'X-Case': 'round-trip' } });
    expect(calls[0]?.headers.get('x-suite')).toBe('checkout');
    expect(calls[0]?.headers.get('x-case')).toBe('round-trip');
    expect(calls[0]?.headers.get('authorization')).toBe('Token s3cret');
  });
});

describe('the bodies the client sends', () => {
  it('sends a stub mapping as JSON', async () => {
    const { client: c, calls } = client({});
    await c.mappings.create({
      request: { method: 'GET', urlPath: '/api/orders' },
      response: { status: 200, body: 'ok' },
    });
    expect(calls[0]?.method).toBe('POST');
    expect(calls[0]?.headers.get('content-type')).toBe('application/json');
    expect(JSON.parse(String(calls[0]?.body))).toEqual({
      request: { method: 'GET', urlPath: '/api/orders' },
      response: { status: 200, body: 'ok' },
    });
  });

  it('wraps a scenario state in the document the endpoint takes', async () => {
    const { client: c, calls } = client({});
    await c.scenarios.setState('order-flow', 'paid');
    expect(calls[0]?.method).toBe('PUT');
    expect(JSON.parse(String(calls[0]?.body))).toEqual({ state: 'paid' });
  });

  it('uploads a file’s bytes rather than a JSON rendering of them', async () => {
    const stub = stubFetch(() => bodyless(201));
    const c = new MockulusClient({ baseUrl: 'http://mock.test:9090', fetch: stub.fetch });

    const bytes = new Uint8Array([0x7b, 0x7d, 0x00, 0xff]);
    await c.files.put('fixtures/blob.bin', bytes.buffer);

    // An `ArrayBuffer` handed to `JSON.stringify` becomes `{}`: a 201 storing
    // two bytes that are not the caller's, with nothing anywhere to say so.
    const call = only(stub.calls);
    expect(call.headers.get('content-type')).toBe('application/octet-stream');
    expect(call.body).toBeInstanceOf(Uint8Array);
    expect(Array.from(call.body as Uint8Array)).toEqual([0x7b, 0x7d, 0x00, 0xff]);
  });

  it('uploads the window of a view, whatever kind of view it is', async () => {
    const stub = stubFetch(() => bodyless(201));
    const c = new MockulusClient({ baseUrl: 'http://mock.test:9090', fetch: stub.fetch });

    // A `DataView` is neither an `ArrayBuffer` nor a `Uint8Array`, so a client
    // that recognised only those would JSON-encode it; and it carries an offset
    // and a length that uploading the buffer behind it would ignore.
    const buffer = new Uint8Array([1, 2, 3, 4, 5]).slice().buffer;
    await c.files.put('slice.bin', new DataView(buffer, 1, 2));

    const call = only(stub.calls);
    expect(call.body).toBeInstanceOf(Uint8Array);
    expect(Array.from(call.body as Uint8Array)).toEqual([2, 3]);
  });

  it('sends the batch import to its own endpoint and reads no report back', async () => {
    const stub = stubFetch(() => bodyless(200));
    const c = new MockulusClient({ baseUrl: 'http://mock.test:9090', fetch: stub.fetch });

    await expect(
      c.mappings.import({
        mappings: [{ request: { method: 'GET', urlPath: '/x' } }],
        importOptions: { duplicatePolicy: 'IGNORE' },
      }),
    ).resolves.toBeUndefined();

    expect(target(stub.calls[0])).toBe('/__admin/mappings/import');
    expect(JSON.parse(String(stub.calls[0]?.body))).toEqual({
      mappings: [{ request: { method: 'GET', urlPath: '/x' } }],
      importOptions: { duplicatePolicy: 'IGNORE' },
    });
  });
});
