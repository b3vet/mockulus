// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { startServer, type Server } from './harness.js';

// The harness is the thing every other integration case depends on, so it is
// worth one case of its own: a suite whose server never started reports as a
// wall of connection refusals, which says nothing about the cause.
describe('the integration harness', () => {
  let server: Server;
  beforeAll(async () => {
    server = await startServer();
  });
  afterAll(async () => {
    await server?.stop();
  });

  it('starts a server and reports dialable addresses', async () => {
    expect(server.adminUrl).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/);
    expect(server.mockUrl).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/);
    expect(server.adminUrl).not.toBe(server.mockUrl);
  });

  it('is a mockulus, and says so', async () => {
    const res = await fetch(`${server.adminUrl}/__admin/version`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as Record<string, unknown>;
    // The same identity assertion the differential harness makes, for the same
    // reason: a suite must not derive anything from a service it has not
    // confirmed is the one it started.
    expect(body['guessedWireMockVersion']).toBeTypeOf('string');
  });

  it('honours an environment overlay', async () => {
    const journaled = await startServer({ MOCKULUS_JOURNAL_ENABLED: 'true' });
    try {
      const res = await fetch(`${journaled.adminUrl}/__admin/requests`);
      // With the journal off this is 500 code 1010, so a 200 is the overlay
      // having taken effect rather than a default that happened to agree.
      expect(res.status).toBe(200);
    } finally {
      await journaled.stop();
    }
  });
});
