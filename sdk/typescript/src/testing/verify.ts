// SPDX-License-Identifier: Apache-2.0

/**
 * Verification that survives the journal being eventually consistent.
 *
 * `POST /__admin/requests/count` answers what the journal holds *now*, and an
 * entry becomes visible within `journal_flush_interval` plus index lag —
 * typically under 500 ms, and bounded by nothing the client can observe (SPEC
 * §11.4, deviation #10). So a verification issued immediately after the traffic
 * it verifies has a race in it: it passes on an idle laptop and fails in CI,
 * and the failure looks like a server defect rather than like an assertion that
 * asked too early.
 *
 * Every consumer discovers this once and papers over it with a sleep, which is
 * both slower than it needs to be and still wrong on a bad day. {@link verify}
 * is that discovery, made once and made properly: it polls until the
 * expectation holds or a deadline passes, and when it gives up it says what it
 * saw along the way rather than only what it saw last.
 */

import { isMockulusError, type MockulusError } from '../errors.js';
import type { JournalWindowOptions } from '../client/shared.js';
import type { MockulusClient } from '../client/client.js';
import type { RequestPattern } from '../types.js';
import { delay, pollBounds, type PollOptions } from './poll.js';

/** A verification expecting a precise number of matching requests. */
interface ExactCount {
  /**
   * Exactly this many matching requests, which is WireMock's `verify(n, …)`.
   *
   * Zero is the negative assertion and is treated differently from every other
   * number, because it is satisfied by the journal's *initial* state: a helper
   * that returned the moment it saw zero would be asserting nothing at all
   * about traffic that has not yet become visible. `times: 0` therefore polls
   * for the whole `within` window and succeeds only if the count was zero at
   * every attempt, so a negative verification costs the visibility window it
   * needs in order to mean anything.
   */
  times?: number;
  atLeast?: never;
}

/** A verification expecting at least a given number of matching requests. */
interface MinimumCount {
  /**
   * At least this many matching requests. Defaults to `1`, which is what
   * WireMock's bare `verify(…)` asserts.
   */
  atLeast?: number;
  times?: never;
}

/**
 * How many matching requests {@link verify} expects.
 *
 * The two are mutually exclusive in the type rather than merely in the
 * documentation, because `{ times: 2, atLeast: 1 }` is not a stricter
 * verification — it is two verifications, one of which the implementation would
 * have to silently ignore. Choosing one quietly is the accept-and-ignore
 * failure this project refuses everywhere else.
 */
export type CountExpectation = ExactCount | MinimumCount;

/** Everything {@link verify} accepts: an expectation, a deadline, and the journal window. */
export type VerifyOptions = JournalWindowOptions & PollOptions & CountExpectation;

/** One answer the journal gave while {@link verify} was waiting. */
export interface Observation {
  /** Milliseconds after the verification started. */
  readonly atMs: number;
  /** What `requests/count` answered then. */
  readonly count: number;
}

/**
 * A verification that did not hold, or could not be run at all.
 *
 * {@link observations} is the whole count history rather than the final number,
 * because the two failures a verification has look identical from the last
 * count alone and want opposite investigations. A count that never moved off
 * zero means the traffic did not match the criteria — the request went
 * somewhere else, or a header criterion is stricter than the caller thinks —
 * and the near-miss endpoints will say which. A count that climbed and stopped
 * short means the traffic matched and there was less of it than expected, which
 * is a question about the system under test rather than about the assertion.
 */
export class VerificationError extends Error {
  /** The pattern that was being counted. */
  readonly criteria: RequestPattern;
  /** The expectation, spelled the way the message spells it. */
  readonly expectation: string;
  /** Every count observed, oldest first. Empty when the journal was disabled. */
  readonly observations: readonly Observation[];
  /** How long the verification spent before giving up. */
  readonly elapsedMs: number;
  /**
   * Whether this is the configuration answer rather than a failed assertion.
   *
   * Worth branching on for the same reason `MockulusError.isJournalDisabled`
   * is: no amount of retrying, and no change to the system under test, will
   * turn it into a pass. The fix is a deployment setting.
   */
  readonly journalDisabled: boolean;

  constructor(init: {
    message: string;
    criteria: RequestPattern;
    expectation: string;
    observations: readonly Observation[];
    elapsedMs: number;
    journalDisabled: boolean;
    cause?: unknown;
  }) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = 'VerificationError';
    this.criteria = init.criteria;
    this.expectation = init.expectation;
    this.observations = init.observations;
    this.elapsedMs = init.elapsedMs;
    this.journalDisabled = init.journalDisabled;
  }
}

/**
 * Waits until the journal holds the requests a pattern describes, and answers
 * how many it held.
 *
 * ```ts
 * await verify(client, { method: 'GET', urlPath: '/orders' }, { times: 2 });
 * ```
 *
 * The criteria are an ordinary {@link RequestPattern} — the same model a stub's
 * `request` is, compiled by the server through the same compiler — so a
 * verification copied from the stub it is about describes the same requests
 * that stub matches. Two models would let a suite pass against a stub that
 * would never have matched anything.
 *
 * What it does with the answer:
 *
 * - **`atLeast: n`** (and the default, `atLeast: 1`) returns as soon as the
 *   count reaches `n`. A lower bound cannot be un-met by a later entry, so
 *   there is nothing to gain by waiting longer.
 * - **`times: n`** for a positive `n` returns as soon as the count *is* `n`,
 *   and fails immediately if it goes past — counts do not fall while a suite
 *   runs, so an overshoot is a decided failure and reporting it at once beats
 *   reporting it two seconds later in the same words.
 * - **`times: 0`** polls for the whole window, for the reason
 *   {@link ExactCount.times} gives: a zero is true before anything has had time
 *   to become visible, so it is only worth asserting after the window in which
 *   it could have stopped being true.
 *
 * Errors other than the journal being disabled are re-thrown as they arrive
 * rather than retried. A 401, a 422 on an unreadable `since`, or a connection
 * that is refused are all statements about the call rather than about the
 * journal's contents, and polling one of them for two seconds only delays the
 * message that already said what was wrong.
 *
 * On a **shared deployment** the journal is one namespace like everything else,
 * so criteria that name a URL another suite also serves will count their
 * traffic too. Verify against URLs a {@link Suite} namespaces, and narrow with
 * `since` when the same URL is used across runs.
 */
export async function verify(
  client: MockulusClient,
  criteria: RequestPattern,
  options: VerifyOptions = {},
): Promise<number> {
  const { within, interval } = pollBounds(options, 'verify');
  const expectation = readExpectation(options);
  const window = journalWindow(options);

  const started = Date.now();
  const deadline = started + within;
  const observations: Observation[] = [];

  for (;;) {
    let count: number;
    try {
      count = (await client.requests.count(criteria, window)).count;
    } catch (err) {
      if (isMockulusError(err) && err.isJournalDisabled) {
        throw journalDisabled(err, criteria, expectation, Date.now() - started);
      }
      throw err;
    }

    const now = Date.now();
    observations.push({ atMs: now - started, count });
    const holds = expectation.holds(count);
    const expired = now >= deadline;

    // An overshoot on an exact expectation is decided: the journal only gains
    // entries while a suite is running, so a count already past the number will
    // not come back to it. Failing here rather than at the deadline is the same
    // message sooner. (A deployment where someone else calls the deployment-wide
    // `DELETE /__admin/requests` mid-run could in principle bring it back down,
    // which is one more reason SPEC §1 asks suites not to.)
    if (!holds && expectation.overshot(count)) {
      throw failure(criteria, expectation, observations, now - started, within);
    }
    // A zero expectation is the one that has to outlast its own window; every
    // other satisfied expectation is answered as soon as it is satisfied.
    if (holds && !(expectation.waitOutWindow && !expired)) {
      return count;
    }
    if (expired) {
      throw failure(criteria, expectation, observations, now - started, within);
    }

    // Shortened so the last attempt lands on the deadline rather than past it:
    // `within` is then the time this takes, not that rounded up to a multiple
    // of `interval`.
    await delay(Math.min(interval, deadline - now), options.signal);
  }
}

/** The expectation, as the loop and the message both need it. */
interface Expectation {
  /** How the message names it: `exactly 2`, `at least 1`. */
  readonly text: string;
  /** Whether a count satisfies it. */
  holds(count: number): boolean;
  /** Whether a count has passed it in the direction it can never come back from. */
  overshot(count: number): boolean;
  /** Whether being satisfied is only meaningful once the whole window has passed. */
  readonly waitOutWindow: boolean;
}

/**
 * Reads the expectation out of the options, refusing the two that are refused
 * in the type as well.
 *
 * The runtime checks are not redundant with the types. This package is
 * published as JavaScript with types beside it, so a JavaScript consumer — or a
 * TypeScript one who reached the call through `any` — gets the same refusal
 * rather than a verification that silently ignored half of what it was asked.
 */
function readExpectation(options: VerifyOptions): Expectation {
  const { times, atLeast } = options;
  if (times !== undefined && atLeast !== undefined) {
    throw new TypeError(
      'verify: `times` and `atLeast` are two different expectations; pass one. ' +
        '`times` is exactly that many, `atLeast` is that many or more.',
    );
  }
  if (times !== undefined) {
    requireCount(times, 'times');
    return {
      text: `exactly ${times}`,
      holds: (count) => count === times,
      overshot: (count) => count > times,
      waitOutWindow: times === 0,
    };
  }
  const minimum = atLeast ?? 1;
  requireCount(minimum, 'atLeast');
  return {
    text: `at least ${minimum}`,
    holds: (count) => count >= minimum,
    // A lower bound has no upper side to overshoot.
    overshot: () => false,
    waitOutWindow: false,
  };
}

/** Refuses a count that is not one — a fraction or a negative can never be met. */
function requireCount(value: number, field: string): void {
  if (!Number.isInteger(value) || value < 0) {
    throw new TypeError(
      `verify: \`${field}\` must be a non-negative integer number of requests, got ${String(value)}`,
    );
  }
}

/**
 * Splits the journal-window and per-call options off {@link VerifyOptions}.
 *
 * Written out field by field rather than spread, because `exactOptionalPropertyTypes`
 * makes `{ since: undefined }` a different thing from `{}` — and it is a
 * different thing on the wire too, where `since=undefined` would reach the
 * server as a timestamp it refuses with a 422.
 */
function journalWindow(options: VerifyOptions): JournalWindowOptions {
  const window: JournalWindowOptions = {};
  if (options.since !== undefined) window.since = options.since;
  if (options.signal !== undefined) window.signal = options.signal;
  if (options.headers !== undefined) window.headers = options.headers;
  return window;
}

/** The failure a verification that ran and did not hold throws. */
function failure(
  criteria: RequestPattern,
  expectation: Expectation,
  observations: readonly Observation[],
  elapsedMs: number,
  within: number,
): VerificationError {
  const last = observations[observations.length - 1]?.count ?? 0;
  const message =
    `verify(${expectation.text}) failed after ${elapsedMs} ms: the journal holds ${last} ` +
    `request(s) matching ${describe(criteria)}.\n` +
    `Counts observed while polling: ${renderHistory(observations)}.\n` +
    `The journal is eventually consistent, so this polled for up to ${within} ms rather than ` +
    `asking once (SPEC §11.4, deviation #10). A count that never moved usually means the ` +
    `traffic did not match these criteria rather than that it never arrived — ` +
    `\`requests.unmatchedNearMisses()\` says which criterion was the one that missed.`;
  return new VerificationError({
    message,
    criteria,
    expectation: expectation.text,
    observations,
    elapsedMs,
    journalDisabled: false,
  });
}

/**
 * The failure for a deployment whose journal is switched off.
 *
 * It gets its own message because the generic one would send the reader to
 * debug an assertion when the answer is a configuration key. The journal is off
 * by default — mockulus makes every expensive feature pay-per-use, where
 * WireMock has this one on (deviation #1) — so this is the *first* thing a
 * verification does against a deployment nobody has configured for it, and the
 * message has to be the one that ends the investigation rather than starts it.
 */
function journalDisabled(
  cause: MockulusError,
  criteria: RequestPattern,
  expectation: Expectation,
  elapsedMs: number,
): VerificationError {
  const message =
    `verify(${expectation.text}) cannot run: this deployment's request journal is disabled, ` +
    `so POST /__admin/requests/count answers 500 with code 1010 instead of a count.\n` +
    `Set \`journal_enabled: true\` — the environment variable is ` +
    `MOCKULUS_JOURNAL_ENABLED=true — and restart the deployment: it is start-up ` +
    `configuration and no admin call turns it on.\n` +
    `The journal is off by default because mockulus makes every expensive feature ` +
    `pay-per-use, which is a deliberate difference from WireMock (deviation #1).`;
  return new VerificationError({
    message,
    criteria,
    expectation: expectation.text,
    observations: [],
    elapsedMs,
    journalDisabled: true,
    cause,
  });
}

/**
 * Renders the count history compactly and without dropping any of it.
 *
 * Consecutive equal counts are collapsed into a run rather than truncated,
 * because both halves of the history are load-bearing: the number tells the
 * reader what was seen and the span tells them whether it was still moving when
 * the deadline arrived. A list of twenty identical zeroes says the same thing
 * far less legibly than `0 (20 polls over 0–2003 ms)`.
 */
function renderHistory(observations: readonly Observation[]): string {
  const runs: { count: number; from: number; to: number; polls: number }[] = [];
  for (const observation of observations) {
    const open = runs[runs.length - 1];
    if (open && open.count === observation.count) {
      open.to = observation.atMs;
      open.polls += 1;
      continue;
    }
    runs.push({ count: observation.count, from: observation.atMs, to: observation.atMs, polls: 1 });
  }
  if (runs.length === 0) return 'none';
  return runs
    .map((run) =>
      run.polls === 1
        ? `${run.count} at ${run.from} ms`
        : `${run.count} (${run.polls} polls over ${run.from}–${run.to} ms)`,
    )
    .join(', ');
}

/** The criteria, short enough to read in a message. */
function describe(criteria: RequestPattern): string {
  const json = JSON.stringify(criteria);
  return json.length <= 300 ? json : `${json.slice(0, 300)}…`;
}
