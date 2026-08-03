// SPDX-License-Identifier: Apache-2.0

/**
 * {@link waitForStub}, against a fake clock and a fake client.
 *
 * The behaviour worth pinning is which not-found it waits through. An unknown
 * stub id is a **bodyless** 404 — the status is the whole message — and that is
 * the answer a stub which has not propagated to this replica yet gives. Every
 * other 404 on this surface carries the error envelope and says something a
 * caller needs to read now rather than in two seconds' time, and a helper that
 * polled through those would turn "this is not a mockulus" into a timeout.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { MockulusClient, waitForStub } from '../../src/index.js';
import { bodyless, errors, json, stubFetch } from './stub-fetch.js';
import { ErrorCode } from '../../src/codes.js';

const id = '11111111-2222-3333-4444-555555555555';

/** A client whose mappings endpoint answers each of `responses` in turn, then repeats the last. */
function replicaAnswering(...responses: (() => Response)[]) {
  let asked = 0;
  const { calls, fetch } = stubFetch(() => {
    const make = responses[Math.min(asked, responses.length - 1)];
    asked += 1;
    if (!make) throw new Error('a replica needs at least one answer');
    return make();
  });
  return { calls, client: new MockulusClient({ baseUrl: 'http://replica.test', fetch }) };
}

const found = () => json(200, { id, response: { status: 200 } });
const absent = () => bodyless(404);

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('waiting for a stub to reach the replica being talked to', () => {
  it('answers the stub as soon as the id resolves', async () => {
    const { calls, client } = replicaAnswering(found);
    await expect(waitForStub(client, id)).resolves.toMatchObject({ id });
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.pathname).toBe(`/__admin/mappings/${id}`);
  });

  it('polls through the bodyless 404 a stub that has not propagated yet gives', async () => {
    const { calls, client } = replicaAnswering(absent, absent, found);
    const settled = waitForStub(client, id).then(
      (stub) => stub.id,
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(200);
    expect(await settled).toBe(id);
    expect(calls).toHaveLength(3);
  });

  it('gives up at the deadline and names the interval that bounds propagation', async () => {
    const { calls, client } = replicaAnswering(absent);
    const settled = waitForStub(client, id, { within: 1000, interval: 250 }).then(
      () => undefined,
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(1000);

    const error = await settled;
    expect(String(error)).toContain('still not visible after 1000 ms and 5 attempt(s)');
    // `sync_interval` is the number a reader needs: a deployment that widened
    // it has not got a defect, it has got a deadline that is too short.
    expect(String(error)).toContain('sync_interval');
    expect(calls).toHaveLength(5);
  });

  it('passes a 404 that carries an envelope straight through', async () => {
    // Not the bodyless not-found. This one says the route does not exist, which
    // means the base URL is pointed at something that is not a mockulus admin
    // API — a fact no amount of waiting improves.
    const { calls, client } = replicaAnswering(() =>
      errors(404, { code: ErrorCode.UnsupportedEndpoint, detail: 'no such endpoint' }),
    );
    await expect(waitForStub(client, id)).rejects.toThrow(/1001/);
    expect(calls).toHaveLength(1);
  });

  it('refuses a poll interval of zero before it makes a single call', async () => {
    const { calls, client } = replicaAnswering(found);
    await expect(waitForStub(client, id, { interval: 0 })).rejects.toThrow(
      /`interval` must be a positive number/,
    );
    expect(calls).toHaveLength(0);
  });
});
