// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { ErrorCode, isMockulusError, MockulusClient } from '../../src/index.js';
import { startServer, type Server } from './harness.js';

/**
 * The admin token, against a deployment that requires one.
 *
 * A separate process, because `admin_auth_token` is start-up configuration.
 * The check wraps the whole router rather than each handler, so one refused
 * call and one accepted call are enough to pin the behaviour for every route —
 * but the *scheme word* is worth its own case, since `Token` rather than
 * `Bearer` is the single detail a client written from habit gets wrong, and the
 * failure it produces is a 401 that looks like a wrong secret.
 */
describe('a deployment with an admin token', () => {
  const token = 'sdk-it-secret-token';
  let server: Server;

  beforeAll(async () => {
    server = await startServer({ MOCKULUS_ADMIN_AUTH_TOKEN: token });
  });

  afterAll(async () => {
    await server?.stop();
  });

  it('refuses a client that carries no token', async () => {
    const client = new MockulusClient({ baseUrl: server.adminUrl });
    const err = await client.mappings.list().catch((e: unknown) => e);

    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.status).toBe(401);
    expect(err.isUnauthorized).toBe(true);
    expect(err.code).toBe(ErrorCode.Malformed);
  });

  it('refuses a token sent under the wrong scheme', async () => {
    // `Bearer` is what a client library reaches for by default and what
    // WireMock does not use. The server refuses it like any other wrong value,
    // which is why the client spells the scheme itself rather than leaving it
    // to the caller's header.
    const client = new MockulusClient({
      baseUrl: server.adminUrl,
      headers: { Authorization: `Bearer ${token}` },
    });
    const err = await client.mappings.list().catch((e: unknown) => e);

    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.isUnauthorized).toBe(true);
  });

  it('accepts the client the token was configured for, on every namespace', async () => {
    const client = new MockulusClient({ baseUrl: server.adminUrl, token });

    // One call per namespace that needs no journal, because the token check
    // wraps the router: a route reachable without it would be a hole in the
    // whole surface rather than in one handler.
    expect((await client.system.version()).guessedWireMockVersion).toBe('3.x-subset');
    expect((await client.system.health()).status).toBe('healthy');
    await expect(client.mappings.list()).resolves.toBeDefined();
    await expect(client.scenarios.list()).resolves.toBeDefined();
    await expect(client.files.list()).resolves.toBeDefined();
    await expect(client.settings.get()).resolves.toBeDefined();

    const created = await client.mappings.create({
      request: { method: 'GET', urlPath: '/sdk-it/auth/order' },
      response: { status: 200 },
    });
    expect((await fetch(server.mockUrl + '/sdk-it/auth/order')).status).toBe(200);
    await client.mappings.delete(created.id ?? '');
  });

  it('does not put the token on the mock port’s own traffic', async () => {
    // The token guards the admin API, not the mock. A suite whose served
    // traffic suddenly needed a header would be a suite that cannot mock an
    // unauthenticated client, which is most of them.
    const client = new MockulusClient({ baseUrl: server.adminUrl, token });
    const created = await client.mappings.create({
      request: { method: 'GET', urlPath: '/sdk-it/auth/open' },
      response: { status: 200, body: 'open' },
    });
    try {
      const served = await fetch(server.mockUrl + '/sdk-it/auth/open');
      expect(served.status).toBe(200);
      expect(await served.text()).toBe('open');
    } finally {
      await client.mappings.delete(created.id ?? '');
    }
  });
});
