// SPDX-License-Identifier: Apache-2.0

/**
 * The shared-deployment discipline, proved on a deployment that is being
 * shared.
 *
 * The claim {@link suite} makes is not that it can register and delete stubs —
 * the client already does that — it is that its cleanup takes **its own** stubs
 * and nothing else. That claim has exactly one interesting failure, and it is
 * the one a shortcut produces: a `cleanup()` that called `POST /__admin/reset`
 * or `DELETE /__admin/mappings` would pass every assertion about the suite's
 * own stubs being gone, and would have destroyed every other runner's work on
 * the way.
 *
 * So the case is built around stubs the suite does not own. One carries no
 * metadata at all and one carries another suite's tag, because those are the
 * two ways a stub on a shared deployment can be somebody else's, and both are
 * asserted to survive — as registered documents *and* as stubs that still
 * serve, since a stub can be deleted from the store and a stub can be swept out
 * of a replica's snapshot, and a reset does both.
 */

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import {
  MockulusClient,
  aResponse,
  get,
  stubFor,
  suite,
  urlPathEqualTo,
  waitForStub,
  type StubMapping,
} from '../../src/index.js';
import { startServer, type Server } from './harness.js';

describe('a suite sharing a deployment with other runners', () => {
  let server: Server;
  let client: MockulusClient;

  /** A stub nobody tagged, which is what most of a shared deployment looks like. */
  let untagged: StubMapping;
  /** A stub another suite registered and expects to clean up itself. */
  let otherSuite: StubMapping;

  const untaggedPath = '/sdk-outsider/untagged';
  const otherSuitePath = '/sdk-outsider/other-suite';

  beforeAll(async () => {
    server = await startServer();
    client = new MockulusClient({ baseUrl: server.adminUrl });
    expect((await client.system.version()).guessedWireMockVersion).toBe('3.x-subset');

    untagged = await client.mappings.create({
      request: { method: 'GET', urlPath: untaggedPath },
      response: { status: 200, body: 'outsider' },
    });
    otherSuite = await client.mappings.create({
      metadata: { suite: 'someone-elses-run', team: 'orders' },
      request: { method: 'GET', urlPath: otherSuitePath },
      response: { status: 200, body: 'other suite' },
    });
  });

  afterAll(async () => {
    await server?.stop();
  });

  it('registers, serves and cleans up without disturbing a stub registered outside it', async () => {
    const run = suite(client, { prefix: 'checkout' });

    const orders = await run.register(
      stubFor(
        get(urlPathEqualTo(run.url('/orders'))).willReturn(
          aResponse().withStatus(200).withJsonBody({ orders: [] }),
        ),
      ),
    );
    await run.register(
      stubFor(get(urlPathEqualTo(run.url('/customers'))).willReturn(aResponse().withStatus(204))),
    );

    // The stub the suite registered is installed on the replica this client is
    // talking to before anything is asserted about what it serves.
    await waitForStub(client, orders.id ?? '');

    // It serves, and it serves under the namespaced URL rather than the bare
    // one — which is what stops two concurrent runs of this suite answering
    // each other's traffic.
    expect((await fetch(server.mockUrl + run.url('/orders'))).status).toBe(200);
    expect((await fetch(server.mockUrl + run.url('/customers'))).status).toBe(204);
    expect((await fetch(server.mockUrl + '/orders')).status).toBe(404);

    // The tag is on every stub, and it is the tag the cleanup will match.
    const mine = await run.stubs();
    expect(mine).toHaveLength(2);
    expect(mine.every((stub) => stub.metadata?.['suite'] === run.id)).toBe(true);

    const removed = await run.cleanup();
    expect(removed).toHaveLength(2);
    expect(await run.stubs()).toHaveLength(0);
    expect((await fetch(server.mockUrl + run.url('/orders'))).status).toBe(404);

    // The whole claim. A `cleanup()` that reached for a global reset would have
    // satisfied every assertion above and failed every assertion below, which
    // is why these are here rather than a count of what the suite owned.
    expect(await client.mappings.getOrNull(untagged.id ?? '')).not.toBeNull();
    expect(await client.mappings.getOrNull(otherSuite.id ?? '')).not.toBeNull();
    const stillServing = await fetch(server.mockUrl + untaggedPath);
    expect(stillServing.status).toBe(200);
    expect(await stillServing.text()).toBe('outsider');
    expect((await fetch(server.mockUrl + otherSuitePath)).status).toBe(200);
  });

  it('leaves another suite alone even when both are running at once', async () => {
    const mine = suite(client, { prefix: 'checkout' });
    const theirs = suite(client, { prefix: 'checkout' });
    // Same prefix, different runs — a CI matrix running one file on two shards.
    expect(theirs.id).not.toBe(mine.id);

    await mine.register(stubFor(get(urlPathEqualTo(mine.url('/orders')))));
    await theirs.register(stubFor(get(urlPathEqualTo(theirs.url('/orders')))));

    expect(await mine.cleanup()).toHaveLength(1);
    // The URLs differ as well as the tags, so the two runs were never serving
    // each other's traffic in the first place — the cleanup is only the last
    // place the separation has to hold.
    expect(await theirs.stubs()).toHaveLength(1);
    expect(await theirs.cleanup()).toHaveLength(1);
  });

  it('is safe to run twice, and on a suite that registered nothing', async () => {
    const run = suite(client, { prefix: 'empty' });
    // `remove-by-metadata` considers only stubs that *have* metadata (deviation
    // #20), so a matcher that finds nothing removes nothing rather than
    // sweeping every untagged stub on the deployment — which is the reading
    // WireMock takes and the reason this cleanup path is safe at all.
    expect(await run.cleanup()).toHaveLength(0);
    expect(await run.cleanup()).toHaveLength(0);
    expect(await client.mappings.getOrNull(untagged.id ?? '')).not.toBeNull();
  });

  it('refuses to re-tag a stub that belongs to another suite', async () => {
    const run = suite(client, { prefix: 'checkout' });
    await expect(
      run.register({
        metadata: { suite: 'someone-elses-run' },
        request: { method: 'GET', urlPath: '/sdk-outsider/never-registered' },
      }),
    ).rejects.toThrow(/already tagged/);
    // Refused before the call, so nothing reached the deployment.
    expect((await fetch(server.mockUrl + '/sdk-outsider/never-registered')).status).toBe(404);
  });
});
