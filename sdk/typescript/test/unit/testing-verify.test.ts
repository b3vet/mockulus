// SPDX-License-Identifier: Apache-2.0

/**
 * The polling logic of {@link verify}, against a fake clock and a fake client.
 *
 * Nothing here sleeps. The whole point of the helper is what it does *over
 * time* — when it stops asking, how often it asks, and what it can say about
 * the answers afterwards — and a case that established any of that by waiting
 * would take as long as the behaviour it describes and would still be at the
 * mercy of a loaded machine. Vitest's fake timers make the passage of time an
 * argument, so a two-second deadline is asserted in microseconds and asserted
 * exactly.
 *
 * The client is real; only the `fetch` under it is a stub. That is deliberate:
 * the journal-off case below depends on a 500 carrying code 1010 being mapped
 * to a `MockulusError` whose `isJournalDisabled` is true, and a hand-written
 * fake client would have asserted that mapping rather than exercised it.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { MockulusClient, VerificationError, isMockulusError, verify } from '../../src/index.js';
import { errors, json, stubFetch } from './stub-fetch.js';

const criteria = { method: 'GET', urlPath: '/orders' } as const;

/**
 * A client whose journal answers the given counts, one per call, holding the
 * last one for every call after the end of the list.
 *
 * Holding rather than failing is what lets a case assert on the count of calls
 * without also having to predict it: a case about the deadline wants the
 * journal to answer the same thing forever, and a case about the interval wants
 * to count how often it was asked.
 */
function journalAnswering(...counts: number[]) {
  let asked = 0;
  const { calls, fetch } = stubFetch(() => {
    const count = counts[Math.min(asked, counts.length - 1)] ?? 0;
    asked += 1;
    return json(200, { count, requestJournalDisabled: false });
  });
  return { calls, client: new MockulusClient({ baseUrl: 'http://journal.test', fetch }) };
}

/** A client whose journal answers one error to every call. */
function journalFailing(status: number, ...problems: { code: number; detail?: string }[]) {
  const { calls, fetch } = stubFetch(() => errors(status, ...problems));
  return { calls, client: new MockulusClient({ baseUrl: 'http://journal.test', fetch }) };
}

/**
 * Watches a promise without waiting on it.
 *
 * A case that drives the clock has to start the work first and advance time
 * afterwards, so the promise is in flight while the case is still running. This
 * attaches handlers immediately — an unobserved rejection is a warning Node
 * prints and, on some runners, an exit code — and reports the outcome as data.
 */
function settle<T>(
  promise: Promise<T>,
): Promise<{ ok: true; value: T } | { ok: false; error: unknown }> {
  return promise.then(
    (value) => ({ ok: true as const, value }),
    (error: unknown) => ({ ok: false as const, error }),
  );
}

/** The {@link VerificationError} an outcome carries, or a failure saying what it carried instead. */
function verificationError(outcome: { ok: boolean; error?: unknown }): VerificationError {
  if (outcome.ok || !(outcome.error instanceof VerificationError)) {
    throw new Error(`expected a VerificationError, got ${String(outcome.error ?? 'success')}`);
  }
  return outcome.error;
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('waiting for the journal to catch up', () => {
  it('answers at once when the expectation already holds', async () => {
    const { calls, client } = journalAnswering(2);
    // No clock is advanced: a journal that is already caught up must not cost a
    // poll interval, which is the whole reason the count is read before the
    // first wait rather than after it.
    await expect(verify(client, criteria, { times: 2 })).resolves.toBe(2);
    expect(calls).toHaveLength(1);
  });

  it('keeps asking until the entry becomes visible', async () => {
    // The shape of deviation #10 in miniature: the traffic has happened, and
    // the journal says zero twice before the flush makes it visible. A helper
    // that asked once would report the first of these three answers.
    const { calls, client } = journalAnswering(0, 0, 2);
    const outcome = settle(verify(client, criteria, { times: 2 }));
    await vi.advanceTimersByTimeAsync(200);
    expect(await outcome).toEqual({ ok: true, value: 2 });
    expect(calls).toHaveLength(3);
  });

  it('asks at the interval it was given', async () => {
    const { calls, client } = journalAnswering(0);
    const outcome = settle(verify(client, criteria, { atLeast: 1, within: 1000, interval: 250 }));
    await vi.advanceTimersByTimeAsync(1000);
    // 0, 250, 500, 750 and 1000 ms — the deadline is a moment the helper asks
    // at rather than one it stops before, so an entry that lands exactly on it
    // still counts.
    expect(calls).toHaveLength(5);
    expect(verificationError(await outcome).elapsedMs).toBe(1000);
  });

  it('shortens the last wait so it gives up on the deadline rather than past it', async () => {
    const { calls, client } = journalAnswering(0);
    const outcome = settle(verify(client, criteria, { atLeast: 1, within: 500, interval: 300 }));
    await vi.advanceTimersByTimeAsync(500);
    // Without the shortening the third attempt would land at 600 ms, and
    // `within` would mean "500 ms rounded up to a multiple of 300" — a
    // deadline a caller cannot predict from the numbers they passed.
    expect(calls).toHaveLength(3);
    expect(verificationError(await outcome).elapsedMs).toBe(500);
  });

  it('stops as soon as a lower bound is met, since nothing can un-meet it', async () => {
    const { calls, client } = journalAnswering(0, 3);
    const outcome = settle(verify(client, criteria, { atLeast: 2 }));
    await vi.advanceTimersByTimeAsync(100);
    expect(await outcome).toEqual({ ok: true, value: 3 });
    expect(calls).toHaveLength(2);
  });

  it('defaults to at least one, which is what a bare WireMock verify asserts', async () => {
    const { client } = journalAnswering(1);
    await expect(verify(client, criteria)).resolves.toBe(1);
  });

  it('sends the journal window on every poll', async () => {
    const since = new Date('2026-08-03T09:00:00.000Z');
    const { calls, client } = journalAnswering(0, 0, 1);
    const outcome = settle(verify(client, criteria, { atLeast: 1, since }));
    await vi.advanceTimersByTimeAsync(200);
    expect(await outcome).toEqual({ ok: true, value: 1 });
    // Every poll, not only the first: a window that was dropped after the first
    // attempt would widen a verification to the whole journal exactly when the
    // helper is doing the waiting the window was written for.
    expect(calls).toHaveLength(3);
    for (const call of calls) {
      expect(call.url.searchParams.get('since')).toBe('2026-08-03T09:00:00.000Z');
    }
  });
});

describe('the failure a verification that does not hold throws', () => {
  it('reports the counts it saw rather than only the last one', async () => {
    const { client } = journalAnswering(0, 0, 0, 1);
    const outcome = settle(verify(client, criteria, { times: 3 }));
    await vi.advanceTimersByTimeAsync(2000);

    const error = verificationError(await outcome);
    expect(error.expectation).toBe('exactly 3');
    expect(error.elapsedMs).toBe(2000);
    expect(error.journalDisabled).toBe(false);
    // The history is the whole point. A count that climbed to 1 and stopped is
    // a different investigation from one that never left zero, and the final
    // number alone cannot tell them apart.
    expect(error.observations.slice(0, 4)).toEqual([
      { atMs: 0, count: 0 },
      { atMs: 100, count: 0 },
      { atMs: 200, count: 0 },
      { atMs: 300, count: 1 },
    ]);
    expect(error.observations).toHaveLength(21);
    expect(error.message).toContain('0 (3 polls over 0–200 ms)');
    expect(error.message).toContain('1 (18 polls over 300–2000 ms)');
    expect(error.message).toContain('"urlPath":"/orders"');
  });

  it('names the criteria and the expectation in the message', async () => {
    const { client } = journalAnswering(0);
    const outcome = settle(verify(client, criteria, { atLeast: 2, within: 0 }));
    await vi.advanceTimersByTimeAsync(0);

    const error = verificationError(await outcome);
    expect(error.message).toContain('verify(at least 2) failed after 0 ms');
    expect(error.criteria).toBe(criteria);
    expect(error.observations).toEqual([{ atMs: 0, count: 0 }]);
  });

  it('fails at once when an exact expectation is overshot', async () => {
    const { calls, client } = journalAnswering(5);
    // The journal only gains entries while a suite runs, so a count already
    // past the number will not come back to it. Waiting out the window would
    // produce the same message two seconds later.
    const error = verificationError(await settle(verify(client, criteria, { times: 2 })));
    expect(calls).toHaveLength(1);
    expect(error.observations).toEqual([{ atMs: 0, count: 5 }]);
    expect(error.message).toContain('the journal holds 5 request(s)');
  });
});

describe('the negative verification', () => {
  it('waits out the whole window, because zero is true before anything arrives', async () => {
    const { calls, client } = journalAnswering(0);
    const outcome = settle(verify(client, criteria, { times: 0, within: 1000, interval: 250 }));

    await vi.advanceTimersByTimeAsync(750);
    // Still running. Returning here would have asserted nothing at all: the
    // journal's initial state satisfies a zero, and the entry this is supposed
    // to rule out may not have been flushed yet.
    expect(calls).toHaveLength(4);

    await vi.advanceTimersByTimeAsync(250);
    expect(await outcome).toEqual({ ok: true, value: 0 });
  });

  it('fails the moment a matching request appears', async () => {
    const { calls, client } = journalAnswering(0, 0, 1);
    const outcome = settle(verify(client, criteria, { times: 0, within: 2000 }));
    await vi.advanceTimersByTimeAsync(200);

    const error = verificationError(await outcome);
    expect(calls).toHaveLength(3);
    expect(error.elapsedMs).toBe(200);
    expect(error.message).toContain('verify(exactly 0) failed');
  });
});

describe('a deployment whose journal is switched off', () => {
  it('says which configuration key to set, and does not retry a settled answer', async () => {
    const { calls, client } = journalFailing(500, {
      code: 1010,
      detail: 'the request journal is disabled',
    });

    const error = verificationError(await settle(verify(client, criteria, { times: 1 })));
    expect(error.journalDisabled).toBe(true);
    // The name of the setting is the whole answer. Without it the reader spends
    // the next twenty minutes on their assertion, because a generic failure
    // looks exactly like a stub that did not match.
    expect(error.message).toContain('journal_enabled');
    expect(error.message).toContain('MOCKULUS_JOURNAL_ENABLED');
    expect(error.message).toContain('1010');
    // Asked once. No amount of polling turns a start-up setting on, and a
    // helper that spent its whole window on it would delay the one message that
    // ends the investigation.
    expect(calls).toHaveLength(1);
    expect(isMockulusError(error.cause)).toBe(true);
  });

  it('leaves every other refusal exactly as the client reported it', async () => {
    const { calls, client } = journalFailing(401, { code: 10, detail: 'not authorised' });

    const outcome = await settle(verify(client, criteria, { times: 1 }));
    expect(outcome.ok).toBe(false);
    if (outcome.ok) throw new Error('unreachable');
    // Wrapping a 401 in a verification failure would bury the status behind a
    // sentence about counts. It is a statement about the call, not about the
    // journal's contents, and polling it for two seconds only delays it.
    expect(outcome.error).not.toBeInstanceOf(VerificationError);
    expect(isMockulusError(outcome.error) && outcome.error.status).toBe(401);
    expect(calls).toHaveLength(1);
  });
});

describe('the options it refuses', () => {
  it('refuses two expectations at once', async () => {
    const { client } = journalAnswering(1);
    // Also a type error — see `testing-type-refusals.ts`. The runtime check is
    // for the JavaScript consumers this package also ships to, who would
    // otherwise get a verification that silently ignored one of the two.
    await expect(
      verify(client, criteria, { times: 2, atLeast: 1 } as { times: number }),
    ).rejects.toThrow(/two different expectations/);
  });

  it('refuses a count that is not a count', async () => {
    const { client } = journalAnswering(1);
    await expect(verify(client, criteria, { times: 1.5 })).rejects.toThrow(
      /`times` must be a non-negative integer/,
    );
    await expect(verify(client, criteria, { atLeast: -1 })).rejects.toThrow(
      /`atLeast` must be a non-negative integer/,
    );
  });

  it('refuses an interval of zero, which is a spin rather than a poll', async () => {
    const { client } = journalAnswering(0);
    await expect(verify(client, criteria, { interval: 0 })).rejects.toThrow(
      /`interval` must be a positive number/,
    );
    await expect(verify(client, criteria, { within: -1 })).rejects.toThrow(
      /`within` must be a non-negative number/,
    );
  });
});

describe('cancellation', () => {
  it('stops waiting when the caller aborts', async () => {
    const { calls, client } = journalAnswering(0);
    const controller = new AbortController();
    const outcome = settle(verify(client, criteria, { atLeast: 1, signal: controller.signal }));

    await vi.advanceTimersByTimeAsync(150);
    expect(calls).toHaveLength(2);
    controller.abort(new Error('the suite was cancelled'));
    await vi.advanceTimersByTimeAsync(1);

    const result = await outcome;
    expect(result.ok).toBe(false);
    // Aborted *during* the wait rather than at the next attempt: a poll loop
    // that sat out its interval before noticing is the difference between a run
    // that ends when it is cancelled and one that looks hung.
    expect(result.ok ? undefined : String(result.error)).toContain('the suite was cancelled');
    expect(calls).toHaveLength(2);
  });
});
