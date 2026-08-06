// SPDX-License-Identifier: Apache-2.0

/**
 * The waiting the two polling helpers share: the deadline, the interval between
 * attempts, and a sleep that can be cancelled.
 *
 * Both of the things these helpers wait for are bounded by a documented
 * interval rather than by a promise the server makes per call — journal
 * visibility by `journal_flush_interval` plus index lag (deviation #10),
 * cross-replica stub propagation by `sync_interval` (deviation #11). Neither
 * bound is observable from the client, so the only correct shape is to ask
 * repeatedly until the answer arrives or a deadline passes. Putting that shape
 * in one place is what stops the two helpers drifting into two slightly
 * different definitions of "within".
 */

/** How long a helper waits, and how often it asks while waiting. */
export interface PollOptions {
  /**
   * How long to keep asking, in milliseconds. Defaults to
   * {@link DEFAULT_WITHIN_MS}.
   *
   * Zero is a legitimate value and means one attempt with no waiting, for a
   * caller who has already established that whatever they are waiting for has
   * happened and wants an assertion rather than a wait.
   */
  within?: number;
  /**
   * How long to wait between attempts, in milliseconds. Defaults to
   * {@link DEFAULT_INTERVAL_MS}.
   *
   * The final wait is shortened so the last attempt lands on the deadline
   * rather than past it, which is what makes `within` the time the helper takes
   * rather than the time it takes rounded up to a multiple of this.
   */
  interval?: number;
}

/**
 * The default deadline, in milliseconds.
 *
 * SPEC §11.4 puts journal visibility under 500 ms in the typical case and the
 * E2E harness verifies after traffic with 2 s windows, so this is the number
 * the server's own regression gate already treats as generous. It is four times
 * the typical case, which leaves room for a loaded CI machine without leaving a
 * genuinely failing verification to hang around for a quarter of a minute — a
 * suite of thirty failing assertions is what makes an over-long default hurt.
 */
export const DEFAULT_WITHIN_MS = 2_000;

/**
 * The default interval between attempts, in milliseconds.
 *
 * Half the default `journal_flush_interval` of 200 ms, so a flush is never
 * waited out twice, and large enough that a two-second deadline costs the admin
 * API twenty requests rather than thousands. A verification is not a hot path,
 * but a suite with hundreds of them shares a deployment with everyone else's.
 */
export const DEFAULT_INTERVAL_MS = 100;

/** Reads {@link PollOptions} into the two numbers the loops need, refusing nonsense. */
export function pollBounds(
  options: PollOptions,
  caller: string,
): { within: number; interval: number } {
  const within = options.within ?? DEFAULT_WITHIN_MS;
  const interval = options.interval ?? DEFAULT_INTERVAL_MS;
  if (!Number.isFinite(within) || within < 0) {
    throw new TypeError(
      `${caller}: \`within\` must be a non-negative number of milliseconds, got ${String(within)}`,
    );
  }
  // Zero is refused rather than clamped because it does not describe a slower
  // or faster poll, it describes a loop with no wait in it at all: the helper
  // would spin against the admin API until the deadline, which on a shared
  // deployment is a denial of service written by accident.
  if (!Number.isFinite(interval) || interval <= 0) {
    throw new TypeError(
      `${caller}: \`interval\` must be a positive number of milliseconds, got ${String(interval)}`,
    );
  }
  return { within, interval };
}

/**
 * Waits, and gives up waiting when the caller's signal is aborted.
 *
 * The signal matters more here than it looks. A caller who aborts a call
 * expects the work to stop, and a poll loop that ignored the abort would sit
 * out the rest of its interval before noticing — which is the difference
 * between a test run that ends when it is cancelled and one that appears to
 * hang. The timer is always cleared and the listener always removed, because a
 * pending timer keeps a Node process alive after the work is finished, and that
 * is how a library makes somebody's suite refuse to exit.
 */
export function delay(ms: number, signal: AbortSignal | undefined): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason as Error);
      return;
    }
    let onAbort: (() => void) | undefined;
    const timer = setTimeout(() => {
      if (signal && onAbort) signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    if (signal) {
      onAbort = () => {
        clearTimeout(timer);
        reject(signal.reason as Error);
      };
      signal.addEventListener('abort', onAbort, { once: true });
    }
  });
}
