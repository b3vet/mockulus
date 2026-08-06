// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { MockulusClient } from '../../src/index.js';
import { startServer, type Server } from './harness.js';

/**
 * The two JSONPath forms the admin UI offers as examples in its metadata search.
 *
 * They are asserted here rather than only shown in the interface because an
 * example is a claim: a reader who types one and gets nothing has been told
 * something false by the product, and there is no gate over interface copy. The
 * server owns the behaviour, so this is where the claim belongs.
 */
describe('find-by-metadata, as the UI documents it', () => {
  let server: Server;
  let client: MockulusClient;
  const prefix = '/sdk-metadata';

  beforeAll(async () => {
    server = await startServer();
    client = new MockulusClient({ baseUrl: server.adminUrl });
    for (const [path, team] of [
      ['/a', 'checkout'],
      ['/b', 'checkout'],
      ['/c', 'platform'],
    ] as const) {
      await client.mappings.create({
        request: { method: 'GET', urlPath: `${prefix}${path}` },
        response: { status: 200 },
        metadata: { team, suite: 'metadata-search' },
      });
    }
    // A stub with no metadata at all, so a presence query is proved to exclude
    // something rather than merely to return everything.
    await client.mappings.create({
      request: { method: 'GET', urlPath: `${prefix}/untagged` },
      response: { status: 200 },
    });
  });

  afterAll(async () => {
    await server?.stop();
  });

  const ours = async (jsonPath: string) => {
    const found = await client.mappings.findByMetadata({ matchesJsonPath: jsonPath });
    return found.mappings.filter((m) => (m.request?.urlPath ?? '').startsWith(prefix));
  };

  it('finds every stub carrying a key, which is what `$.team` means', async () => {
    expect(await ours('$.team')).toHaveLength(3);
  });

  it('finds stubs by a value, which is what the filter form means', async () => {
    expect(await ours("$[?(@.team == 'checkout')]")).toHaveLength(2);
    expect(await ours("$[?(@.team == 'platform')]")).toHaveLength(1);
  });

  it('answers nothing for a key nobody carries, rather than everything', async () => {
    expect(await ours('$.nope')).toHaveLength(0);
  });

  // The interface promises the refusal is shown; that only helps if the server
  // refuses rather than quietly matching nothing, which is the failure mode
  // deviation #5 exists to prevent elsewhere.
  it('refuses an expression that is not a JSONPath', async () => {
    await expect(client.mappings.findByMetadata({ matchesJsonPath: 'garbage' })).rejects.toThrow();
  });
});
