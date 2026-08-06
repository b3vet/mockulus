// SPDX-License-Identifier: Apache-2.0
import { isMockulusError, type MockulusClient } from '@mockulus/admin-sdk';
import type { Api } from './api.svelte';

/**
 * One admin API read, and everything a view needs to render it: whether it is
 * in flight, what came back, and what went wrong.
 *
 * This exists so the 401 rule is written once. Every read in the UI can be
 * refused for want of a token, and the SOW's requirement is that the failed
 * action be retryable without a page reload — which means each read has to know
 * how to run itself again, and has to hand that ability to whoever collects the
 * token. A view that wrote its own `try/catch` would satisfy that in the first
 * place it was written and nowhere else.
 */
export interface Resource<T> {
  /** A request is in flight. */
  readonly loading: boolean;
  /** The last successful answer, or `undefined` before one arrives. */
  readonly data: T | undefined;
  /** The last failure, or `undefined`. Never set at the same time as `data`. */
  readonly error: unknown;
  /** Runs the read again, discarding anything already in flight. */
  reload(): void;
}

export function createResource<T>(
  api: Api,
  load: (client: MockulusClient) => Promise<T>,
): Resource<T> {
  let data = $state<T | undefined>(undefined);
  let error = $state<unknown>(undefined);
  let loading = $state(false);

  /**
   * Which run owns the state. A user who types a filter, waits, and types
   * another has two reads in flight over one slot, and without this the slower
   * of the two wins — showing an answer to a question that is no longer on
   * screen. The counter makes a superseded run resolve into nothing.
   */
  let generation = 0;

  const reload = (): void => {
    const mine = ++generation;
    loading = true;
    error = undefined;

    // Read through `api.client` at call time rather than capturing it: the
    // client is rebuilt when a token arrives, and a retry has to use the new
    // one or it repeats the 401 that queued it.
    void load(api.client).then(
      (value) => {
        if (mine !== generation) return;
        data = value;
        loading = false;
      },
      (err: unknown) => {
        if (mine !== generation) return;
        // Cleared rather than kept, so a view never renders a stale list under
        // an error banner and leaves the reader to work out which is current.
        data = undefined;
        error = err;
        loading = false;
        if (isMockulusError(err) && err.isUnauthorized) {
          api.requestToken(reload);
        }
      },
    );
  };

  reload();

  return {
    get loading() {
      return loading;
    },
    get data() {
      return data;
    },
    get error() {
      return error;
    },
    reload,
  };
}
