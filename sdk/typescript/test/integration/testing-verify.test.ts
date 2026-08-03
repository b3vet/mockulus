// SPDX-License-Identifier: Apache-2.0

/**
 * The verification helpers against a real mockulus.
 *
 * The unit lane pins the polling logic against a fake clock, which is where the
 * timing claims can be asserted exactly. What it cannot say is whether the
 * journal behaves the way the helper assumes — that traffic really does become
 * visible after a delay rather than immediately, that a deployment with no
 * journal really does answer 500 with code 1010, that a count over a criteria
 * pattern really does mean what the stub's own `request` means. Those are
 * statements about the server, and only a server can make them.
 *
 * The journal keeps its **default** `journal_flush_interval` here rather than
 * being shortened to make the suite quick. Shortening it would leave the case
 * passing for a helper that asked once, which is the defect these helpers exist
 * to prevent.
 *
 * Every stub lives under `/sdk-testing/…` and nothing here calls a reset, which
 * is the discipline SPEC §1 asks of anyone sharing a deployment.
 */

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import {
  MockulusClient,
  VerificationError,
  isMockulusError,
  verify,
  waitForStub,
} from '../../src/index.js';
import { startServer, type Server } from './harness.js';

describe('verification against a deployment with a journal', () => {
  let server: Server;
  let client: MockulusClient;

  const served = '/sdk-testing/verify/order';
  const quiet = '/sdk-testing/verify/never-called';

  beforeAll(async () => {
    server = await startServer({ MOCKULUS_JOURNAL_ENABLED: 'true' });
    client = new MockulusClient({ baseUrl: server.adminUrl });
    // Identity before anything is recorded from it: a port answering is not the
    // same as the thing on it being the thing under test.
    expect((await client.system.version()).guessedWireMockVersion).toBe('3.x-subset');

    await client.mappings.create({
      request: { method: 'GET', urlPath: served },
      response: { status: 200, body: 'served' },
    });
  });

  afterAll(async () => {
    await server?.stop();
  });

  it('holds once the traffic it describes has become visible', async () => {
    await fetch(server.mockUrl + served);
    await fetch(server.mockUrl + served);

    // Issued immediately after the traffic, which is exactly the shape that is
    // a race without the polling: the entries are queued and become visible
    // within `journal_flush_interval` plus index lag (deviation #10).
    const count = await verify(client, { method: 'GET', urlPath: served }, { times: 2 });
    expect(count).toBe(2);
  });

  it('accepts the same pattern a stub states its criteria with', async () => {
    // The criteria are compiled by the server through the same compiler as a
    // stub's `request`, so a verification copied from the stub it is about
    // describes the same requests. A header criterion is the part of that worth
    // exercising: it is where two models would first disagree.
    await fetch(server.mockUrl + served, { headers: { Accept: 'application/json' } });
    const count = await verify(
      client,
      { method: 'GET', urlPath: served, headers: { Accept: { contains: 'json' } } },
      { atLeast: 1 },
    );
    expect(count).toBeGreaterThanOrEqual(1);
  });

  it('reports the count history when the expectation is never met', async () => {
    await fetch(server.mockUrl + served);

    const error = await verify(
      client,
      { method: 'GET', urlPath: served },
      // A number the traffic above cannot reach, over a window long enough for
      // every entry that is coming to have arrived — so the failure is the
      // expectation being wrong rather than the helper being impatient.
      { times: 99, within: 1_000, interval: 100 },
    ).catch((err: unknown) => err);

    if (!(error instanceof VerificationError)) {
      throw new Error(`expected a VerificationError, got ${String(error)}`);
    }
    expect(error.journalDisabled).toBe(false);
    expect(error.expectation).toBe('exactly 99');
    expect(error.elapsedMs).toBeGreaterThanOrEqual(1_000);
    // More than one observation, and the message carries them: a failure that
    // reported only the final number would leave the reader unable to tell a
    // count that never moved from one that climbed and stopped short.
    expect(error.observations.length).toBeGreaterThan(1);
    expect(error.message).toContain('Counts observed while polling:');
    expect(error.message).toContain(served);
  });

  it('holds a negative verification across the whole window', async () => {
    // Nothing has ever been sent to this path. The helper still waits out the
    // window, because a zero is satisfied by the journal's initial state and an
    // answer given before anything could have become visible asserts nothing.
    const started = Date.now();
    await expect(
      verify(client, { method: 'GET', urlPath: quiet }, { times: 0, within: 400 }),
    ).resolves.toBe(0);
    expect(Date.now() - started).toBeGreaterThanOrEqual(400);
  });

  it('waits for a freshly registered stub to be visible', async () => {
    const stub = await client.mappings.create({
      request: { method: 'GET', urlPath: '/sdk-testing/verify/propagated' },
      response: { status: 200 },
    });
    // One replica in this topology, so the stub is visible on the write's own
    // answer; the helper's contract is that it copes when there are more, and
    // what this pins is that the happy path costs nothing when there are not.
    await expect(waitForStub(client, stub.id ?? '')).resolves.toMatchObject({ id: stub.id });
  });

  it('reports a stub id that names nothing rather than waiting forever', async () => {
    await expect(
      waitForStub(client, '9f2c1d4e-0000-4000-8000-000000000000', { within: 200 }),
    ).rejects.toThrow(/still not visible after/);
  });
});

describe('verification against a deployment with the journal off', () => {
  let server: Server;
  let client: MockulusClient;

  beforeAll(async () => {
    // No overlay at all: this is what `docker run mockulus` gives you, and
    // therefore what a first verification meets.
    server = await startServer();
    client = new MockulusClient({ baseUrl: server.adminUrl });
    expect((await client.system.version()).guessedWireMockVersion).toBe('3.x-subset');
  });

  afterAll(async () => {
    await server?.stop();
  });

  it('says which configuration key to set instead of reporting a failed assertion', async () => {
    const started = Date.now();
    const error = await verify(client, { method: 'GET', urlPath: '/sdk-testing/off' }).catch(
      (err: unknown) => err,
    );

    if (!(error instanceof VerificationError)) {
      throw new Error(`expected a VerificationError, got ${String(error)}`);
    }
    expect(error.journalDisabled).toBe(true);
    // The name of the key is the answer. A generic failure here sends someone
    // to debug an assertion that is fine, because a journal-off deployment and
    // a stub that never matched look identical from a count of zero — except
    // that this one is not even a count, it is a 500.
    expect(error.message).toContain('journal_enabled');
    expect(error.message).toContain('MOCKULUS_JOURNAL_ENABLED');
    // Answered at once rather than polled: no amount of waiting turns on a
    // start-up setting, and the underlying refusal is carried as the cause so a
    // caller can read the server's own words.
    expect(Date.now() - started).toBeLessThan(1_000);
    expect(isMockulusError(error.cause) && error.cause.status).toBe(500);
    expect(isMockulusError(error.cause) && error.cause.isJournalDisabled).toBe(true);
  });
});
