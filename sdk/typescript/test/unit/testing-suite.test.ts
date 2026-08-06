// SPDX-License-Identifier: Apache-2.0

/**
 * What a {@link Suite} puts on the wire.
 *
 * The claim this helper makes is about the calls it does and does not make, and
 * that is exactly what a stub `fetch` can see: the tag it writes into every
 * `metadata`, the matcher it cleans up with, and — the one that matters — that
 * the path list never contains a reset. The integration lane proves the
 * consequence against a real server, that a stub registered outside the suite
 * survives its cleanup; this lane proves the mechanism, which is what makes a
 * regression here readable rather than merely detected.
 */

import { describe, expect, it } from 'vitest';

import { MockulusClient, suite, type StubMapping } from '../../src/index.js';
import { json, stubFetch } from './stub-fetch.js';

/** A client that echoes a create back and answers the metadata searches with a list. */
function recordingClient(removed: StubMapping[] = []) {
  // Method and path together, because the deployment-wide delete of every stub
  // shares its path with the create that registers one — `/__admin/mappings`
  // says nothing on its own about which of the two was called.
  const paths: string[] = [];
  const { calls, fetch } = stubFetch((call) => {
    paths.push(`${call.method} ${call.url.pathname}`);
    if (call.url.pathname === '/__admin/mappings') {
      // The server answers a create with the document as stored, which is the
      // document that was sent plus an assigned id.
      const sent = JSON.parse(String(call.body)) as StubMapping;
      return json(201, { ...sent, id: '11111111-2222-3333-4444-555555555555' });
    }
    return json(200, { mappings: removed, meta: { total: removed.length } });
  });
  return { calls, paths, client: new MockulusClient({ baseUrl: 'http://mock.test', fetch }) };
}

/** The JSON body of the recorded call at `index`. */
function bodyOf(calls: { body: string | Uint8Array | undefined }[], index: number): unknown {
  return JSON.parse(String(calls[index]?.body));
}

describe('the namespace a suite opens', () => {
  it('puts the run id in the URL, so two runs of one suite can run at once', () => {
    const run = suite(recordingClient().client, { prefix: 'checkout', id: 'checkout-1f4c9a2e' });
    expect(run.basePath).toBe('/checkout-1f4c9a2e');
    expect(run.url('/orders')).toBe('/checkout-1f4c9a2e/orders');
    // A metadata tag is enough to clean up separately. Only a distinct URL is
    // enough for two runners to be registering stubs at the same moment
    // without one of them serving the other's traffic.
    expect(run.url('orders')).toBe('/checkout-1f4c9a2e/orders');
    expect(run.url()).toBe('/checkout-1f4c9a2e');
    expect(run.url('/')).toBe('/checkout-1f4c9a2e');
  });

  it('draws a random suffix when the caller has no id worth using', () => {
    const { client } = recordingClient();
    const first = suite(client, { prefix: 'checkout' });
    const second = suite(client, { prefix: 'checkout' });
    expect(first.id).toMatch(/^checkout-[0-9a-f]{8}$/);
    // Two suites with the same prefix are two namespaces, which is the case a
    // CI matrix running the same file on four shards produces.
    expect(second.id).not.toBe(first.id);
  });

  it('accepts a prefix written as a path and refuses one that would change a URL', () => {
    const { client } = recordingClient();
    expect(suite(client, { prefix: '/checkout/' }).id).toMatch(/^checkout-/);
    expect(suite(client, { prefix: 'team/checkout' }).id).toMatch(/^team-checkout-/);
    // A `?` or a space would either change the meaning of every URL built from
    // it or come back percent-encoded, and both are discovered later by someone
    // who did not write the suite.
    expect(() => suite(client, { prefix: 'check out' })).toThrow(/must be a non-empty name/);
    expect(() => suite(client, { prefix: 'checkout?x=1' })).toThrow(/must be a non-empty name/);
    expect(() => suite(client, { prefix: '/' })).toThrow(/must be a non-empty name/);
  });
});

describe('registering through a suite', () => {
  it('tags every stub with the run id and keeps the caller their own metadata', async () => {
    const { calls, client } = recordingClient();
    const run = suite(client, { prefix: 'checkout', id: 'checkout-1f4c9a2e' });

    await run.register({
      metadata: { team: 'payments' },
      request: { method: 'GET', urlPath: run.url('/orders') },
      response: { status: 200 },
    });

    expect(bodyOf(calls, 0)).toEqual({
      metadata: { team: 'payments', suite: 'checkout-1f4c9a2e' },
      request: { method: 'GET', urlPath: '/checkout-1f4c9a2e/orders' },
      response: { status: 200 },
    });
  });

  it('refuses a stub that another suite already claimed', async () => {
    const { calls, client } = recordingClient();
    const run = suite(client, { prefix: 'checkout', id: 'checkout-1f4c9a2e' });

    // Re-tagging would take the stub out of the cleanup its author arranged and
    // leave it behind on a shared deployment, and the create would have
    // answered 201 either way — so there would be nothing to notice.
    await expect(
      run.register({ metadata: { suite: 'orders-9b2e' }, response: { status: 200 } }),
    ).rejects.toThrow(/already tagged suite="orders-9b2e"/);
    expect(calls).toHaveLength(0);
  });
});

describe('cleaning up after a suite', () => {
  it('removes by metadata and answers with what it removed', async () => {
    const removed: StubMapping[] = [{ id: 'a', response: { status: 200 } }];
    const { calls, paths, client } = recordingClient(removed);
    const run = suite(client, { prefix: 'checkout', id: 'checkout-1f4c9a2e' });

    expect(await run.cleanup()).toEqual(removed);
    expect(paths).toEqual(['POST /__admin/mappings/remove-by-metadata']);
    // `ignoreExtraElements` is what makes the caller's own metadata keys
    // harmless: an equality against the whole document would match only stubs
    // whose metadata is the tag and nothing else.
    expect(bodyOf(calls, 0)).toEqual({
      equalToJson: { suite: 'checkout-1f4c9a2e' },
      ignoreExtraElements: true,
    });
  });

  it('never calls a reset — the one thing this class exists to avoid', async () => {
    const { paths, client } = recordingClient();
    const run = suite(client, { prefix: 'checkout', id: 'checkout-1f4c9a2e' });

    await run.register({ request: { method: 'GET', urlPath: run.url('/a') } });
    await run.stubs();
    await run.cleanup();
    await run.cleanup();

    // On a shared deployment each of these deletes every other runner's stubs,
    // and the damage surfaces half an hour later as somebody else's stub
    // failing to match — which reads as a mockulus defect and is investigated
    // as one. The journal's own `DELETE /__admin/requests` is on the list for
    // the same reason: there is no scoped way to empty it.
    for (const forbidden of [
      'POST /__admin/reset',
      'POST /__admin/mappings/reset',
      'DELETE /__admin/mappings',
      'DELETE /__admin/requests',
      'POST /__admin/scenarios/reset',
    ]) {
      expect(paths).not.toContain(forbidden);
    }
    expect(paths).toEqual([
      'POST /__admin/mappings',
      'POST /__admin/mappings/find-by-metadata',
      'POST /__admin/mappings/remove-by-metadata',
      'POST /__admin/mappings/remove-by-metadata',
    ]);
  });

  it('searches with the same matcher it deletes with', async () => {
    const { calls, paths, client } = recordingClient();
    const run = suite(client, { prefix: 'checkout', id: 'checkout-1f4c9a2e' });

    await run.stubs();
    await run.cleanup();

    expect(paths).toEqual([
      'POST /__admin/mappings/find-by-metadata',
      'POST /__admin/mappings/remove-by-metadata',
    ]);
    // One definition, so the set `stubs()` reports is the set `cleanup()`
    // deletes. Two matchers would eventually disagree, and the disagreement
    // would be a stub left behind that a listing said had gone.
    expect(bodyOf(calls, 0)).toEqual(bodyOf(calls, 1));
  });
});
