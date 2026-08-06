// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { isMockulusError, MockulusClient, type StubMapping } from '../../src/index.js';
import { startServer, type Server } from './harness.js';

/**
 * The journal namespace against a deployment that has one.
 *
 * A separate process, because `journal_enabled` is start-up configuration
 * rather than something an admin call can change — which is itself the reason
 * the sibling file can assert the 1010 path so cheaply.
 *
 * The journal is **eventually consistent**: an entry becomes visible within
 * `journal_flush_interval` plus index lag. Every assertion about what is in it
 * therefore polls for the state it expects rather than asserting once, which is
 * exactly the shape this SDK's test helpers will encode later. Asserting once
 * would produce a suite that passes on a fast machine and fails in CI, and the
 * failure would look like a server defect.
 */
describe('the request journal, switched on', () => {
  let server: Server;
  let client: MockulusClient;
  let stub: StubMapping;

  const matched = '/sdk-it/journal/order';
  const unmatched = '/sdk-it/journal/nothing-serves-this';

  beforeAll(async () => {
    server = await startServer({
      MOCKULUS_JOURNAL_ENABLED: 'true',
      // Shortens the wait rather than removing it. The polling below is what
      // makes the cases correct; this only makes them quick.
      MOCKULUS_JOURNAL_FLUSH_INTERVAL: '20ms',
    });
    client = new MockulusClient({ baseUrl: server.adminUrl });
    expect((await client.system.version()).guessedWireMockVersion).toBe('3.x-subset');

    stub = await client.mappings.create({
      request: { method: 'GET', urlPath: matched },
      response: { status: 200, body: 'served' },
    });

    await fetch(server.mockUrl + matched);
    await fetch(server.mockUrl + matched);
    await fetch(server.mockUrl + unmatched);
  });

  afterAll(async () => {
    await server?.stop();
  });

  it('counts the requests a pattern describes', async () => {
    // The pattern is the same model the stub's `request` is, compiled through
    // the same compiler — which is what makes a verification and the stub it
    // was copied from describe the same requests.
    const count = await eventually('two matched requests in the journal', async () => {
      const answer = await client.requests.count({ method: 'GET', urlPath: matched });
      return answer.count === 2 ? answer : undefined;
    });
    expect(count.count).toBe(2);
    expect(count.requestJournalDisabled).toBe(false);
  });

  it('lists serve events newest first, with a total over the window', async () => {
    const page = await eventually('the journal listing to hold all three calls', async () => {
      const answer = await client.requests.list({ limit: 1 });
      return answer.meta.total >= 3 ? answer : undefined;
    });

    // `limit` trims the page after the window is counted, which is why the
    // total can exceed the array's length — the field a client reads to decide
    // whether it has seen everything.
    expect(page.requests).toHaveLength(1);
    expect(page.meta.total).toBeGreaterThanOrEqual(3);

    const event = page.requests[0];
    expect(event?.id).toBeTypeOf('string');
    expect(event?.request.url).toBeTypeOf('string');
  });

  it('answers find with logged requests and remove with whole serve events', async () => {
    const found = await eventually('find to see both matched calls', async () => {
      const answer = await client.requests.find({ method: 'GET', urlPath: matched });
      return answer.requests.length === 2 ? answer : undefined;
    });
    // Bare logged requests, not the events holding them: a client deserializing
    // this into typed requests would find every field null otherwise.
    expect(found.requests.every((r) => r.url === matched)).toBe(true);
    expect(found.requests[0]?.method).toBe('GET');

    const removed = await client.requests.remove({ method: 'GET', urlPath: matched });
    // The one criteria query that answers with whole events, under
    // `serveEvents` rather than `requests`.
    expect(removed.serveEvents).toHaveLength(2);
    expect(removed.serveEvents[0]?.request.url).toBe(matched);
    expect(removed.serveEvents[0]?.wasMatched).toBe(true);

    const afterRemoval = await client.requests.count({ method: 'GET', urlPath: matched });
    expect(afterRemoval.count).toBe(0);
  });

  it('reports what matched nothing, and what came closest', async () => {
    const missed = await eventually('the unmatched call to be recorded', async () => {
      const answer = await client.requests.unmatched();
      return answer.requests.some((r) => r.url === unmatched) ? answer : undefined;
    });
    expect(missed.requests.map((r) => r.url)).toContain(unmatched);

    const scored = await client.requests.unmatchedNearMisses();
    // One flat list of request-and-stub pairings rather than a list grouped by
    // request, which is the shape WireMock's clients deserialize.
    const closest = scored.nearMisses.find((n) => n.request.url === unmatched);
    expect(closest?.stubMapping?.id).toBe(stub.id);
    expect(closest?.matchResult.distance).toBeGreaterThan(0);
  });

  it('reads and deletes one entry by the id a listing carried', async () => {
    const page = await eventually('an entry to read back by id', async () => {
      const answer = await client.requests.list({ limit: 1 });
      return answer.requests[0] ? answer : undefined;
    });
    const id = page.requests[0]?.id ?? '';

    const one = await client.requests.get(id);
    expect(one.id).toBe(id);

    await client.requests.delete(id);
    await eventually('the deleted entry to be gone', async () => {
      const gone = await client.requests.getOrNull(id);
      return gone === null ? true : undefined;
    });
  });

  it('deletes an entry that was never there without complaining', async () => {
    // The delete is idempotent and deliberately silent about a missing entry:
    // an id naming nothing answers 200 like any other, because the caller's
    // goal — that entry not being in the journal — holds either way. This is
    // the one id route on the surface that does *not* answer 404, and a client
    // written to expect one would report a failure where there is none.
    await expect(client.requests.delete('2VZ8mQ0kZ9pQnR6b1cN3sT7yWxE')).resolves.toBeUndefined();
  });

  it('honours a `since` window and refuses one it cannot parse', async () => {
    // A call the window will exclude, waited for without a window first. Without
    // this the case passes whether or not `since` was sent at all: by the time
    // it runs the earlier cases have emptied the journal, and a count of zero
    // over a window nobody applied is indistinguishable from one that was.
    await fetch(server.mockUrl + matched);
    await eventually('the fresh call to be recorded', async () => {
      const answer = await client.requests.count({ method: 'GET', urlPath: matched });
      return answer.count >= 1 ? answer : undefined;
    });

    const future = new Date(Date.now() + 60 * 60 * 1000);
    const windowed = await client.requests.count(
      { method: 'GET', urlPath: matched },
      { since: future },
    );
    expect(windowed.count).toBe(0);

    const err = await client.requests.list({ since: 'yesterday-ish' }).catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    // Refused rather than dropped: ignoring it would answer a windowed
    // verification with the entire journal, so a `verify(exactly(1))` written
    // to look at the last minute would count every call ever served.
    expect(err.status).toBe(422);
    expect(err.pointers()).toEqual(['since']);
  });

  it('clears the journal without touching the stubs', async () => {
    await client.requests.clear();
    await eventually('the journal to read empty', async () => {
      const answer = await client.requests.list();
      return answer.meta.total === 0 ? true : undefined;
    });
    expect(await client.mappings.getOrNull(stub.id ?? '')).not.toBeNull();
  });
});

/**
 * Polls `probe` until it answers with something, or gives up loudly.
 *
 * `undefined` means "not yet"; anything else is the answer. The message names
 * what was being waited for, because a timeout that says only "timed out" sends
 * the reader to the wrong half of the system.
 */
async function eventually<T>(
  what: string,
  probe: () => Promise<T | undefined>,
  timeoutMs = 10_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const answer = await probe();
    if (answer !== undefined) return answer;
    if (Date.now() >= deadline) {
      throw new Error(`waited ${timeoutMs}ms for ${what} and it never happened`);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
}
