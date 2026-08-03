// SPDX-License-Identifier: Apache-2.0
import { isMockulusError, type MockulusClient } from '@mockulus/admin-sdk';
import type { Api } from './api.svelte';

/**
 * One admin API write, and everything a view needs to render it: whether it is
 * in flight, what it refused, and how to run it again.
 *
 * The sibling of {@link import('./resource.svelte').createResource}, and it
 * exists for the same reason: the 401 rule has to be written once. A write
 * refused for want of a token must open the token sheet and then be retried with
 * the arguments it was originally called with — a Save that lost the document it
 * was saving would be a worse outcome than the 401.
 *
 * A write differs from a read in one way that matters here. A read has no
 * arguments and can re-run itself; a write is a save of *this* document or a
 * delete of *that* stub, so retrying means remembering what was asked. That is
 * the only reason this is not the same function as `createResource`.
 */
export interface Action<A extends readonly unknown[]> {
  /** A call is in flight. */
  readonly pending: boolean;
  /** How the last call failed, or `undefined`. Cleared when the next one starts. */
  readonly error: unknown;
  /** Runs the write. A call while one is in flight supersedes it. */
  run(...args: A): void;
  /** Drops the last failure without running anything — for a view leaving the surface. */
  reset(): void;
}

export function createAction<A extends readonly unknown[], R>(
  api: Api,
  perform: (client: MockulusClient, ...args: A) => Promise<R>,
  onsuccess?: (result: R) => void,
): Action<A> {
  let pending = $state(false);
  let error = $state<unknown>(undefined);

  /**
   * Which call owns the state, on the same argument as the resource's: a user
   * who presses Save twice has two writes over one slot, and without this the
   * slower one decides what is on screen. A superseded call resolves into
   * nothing.
   */
  let generation = 0;

  /** What the last call was given, so the retry after a 401 sends it again. */
  let lastArgs: A | undefined;

  const start = (...args: A): void => {
    lastArgs = args;
    const mine = ++generation;
    pending = true;
    error = undefined;

    // Read through `api.client` at call time rather than capturing it: the
    // client is rebuilt when a token arrives, and a retry has to use the new one
    // or it repeats the 401 that queued it.
    void perform(api.client, ...args).then(
      (result) => {
        if (mine !== generation) return;
        pending = false;
        onsuccess?.(result);
      },
      (err: unknown) => {
        if (mine !== generation) return;
        pending = false;
        error = err;
        if (isMockulusError(err) && err.isUnauthorized) {
          api.requestToken(retry);
        }
      },
    );
  };

  /**
   * A stable function, not a fresh closure per call, because the api dedupes
   * queued retries by identity. Two writes that both 401 should produce one
   * retry of the latest arguments rather than two writes when the token lands.
   */
  const retry = (): void => {
    if (lastArgs !== undefined) {
      start(...lastArgs);
    }
  };

  return {
    get pending() {
      return pending;
    },
    get error() {
      return error;
    },
    run: start,
    reset() {
      // The generation moves so that a call still in flight cannot land on the
      // state a view has just cleared.
      generation += 1;
      pending = false;
      error = undefined;
      lastArgs = undefined;
    },
  };
}
