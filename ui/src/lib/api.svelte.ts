// SPDX-License-Identifier: Apache-2.0
import { MockulusClient, type MockulusClientOptions } from '@mockulus/admin-sdk';
import { getContext, setContext } from 'svelte';

/**
 * The UI's one door to the admin API, and the only place that knows where the
 * admin token is kept.
 *
 * Every call the UI makes goes through `@mockulus/admin-sdk` (SOW decision U5).
 * There is no `fetch` to `/__admin` anywhere in this tree, and there should
 * never be one: the SDK is the typed statement of what the admin surface can be
 * asked, and a call written around it is a call nothing type-checks and nothing
 * updates when the contract moves.
 */

/**
 * Where the token lives between page loads.
 *
 * `sessionStorage`, and deliberately none of the alternatives. `localStorage`
 * would outlive the tab and be readable by every other tab on the origin, which
 * for a credential that opens a whole mock deployment is a longer life than the
 * job needs. A cookie would be attached by the browser to requests the UI did
 * not make, including the page loads that are exempt from the token by design
 * (U4), which turns a header the SDK controls into ambient authority. A URL
 * would put it in history, in the `Referer` of anything the page links to, and
 * in every access log between here and the server.
 */
const TOKEN_STORAGE_KEY = 'mockulus.admin-token';

/** What {@link createApi} needs from the outside, so tests can supply their own. */
export interface ApiOptions {
  /**
   * The admin API's origin. The UI is served same-origin by the admin mux, and
   * in dev vite proxies `/__admin` to a local server, so the page's own origin
   * is right in both — and a hardcoded host would be wrong in both.
   */
  baseUrl?: string;
  /** Where the token is persisted. Defaults to `window.sessionStorage`. */
  storage?: Storage;
  /** Builds the SDK client. Swapped in tests for one that makes no requests. */
  createClient?: (options: MockulusClientOptions) => MockulusClient;
}

export interface Api {
  /** The client to call. Replaced when the token changes, so read it per call. */
  readonly client: MockulusClient;
  /** Whether the token sheet is open. */
  readonly tokenRequested: boolean;
  /** Whether a token is currently held. */
  readonly hasToken: boolean;
  /**
   * Reports that a call was refused for want of a token, and queues the work to
   * re-run once one is supplied.
   */
  requestToken(retry?: () => void): void;
  /** Accepts a token, then re-runs whatever was waiting on it. */
  submitToken(token: string): void;
  /** Closes the sheet without a token, abandoning the queued work. */
  dismissTokenRequest(): void;
  /** Forgets the token, for an operator who entered the wrong one or is done. */
  clearToken(): void;
}

function readStoredToken(storage: Storage): string | undefined {
  try {
    return storage.getItem(TOKEN_STORAGE_KEY) ?? undefined;
  } catch {
    // Storage throws rather than returning null when the browser has it
    // disabled. The UI still works — the token just does not survive a reload —
    // and that is a better answer than a shell that will not mount.
    return undefined;
  }
}

function writeStoredToken(storage: Storage, token: string | undefined): void {
  try {
    if (token === undefined) {
      storage.removeItem(TOKEN_STORAGE_KEY);
    } else {
      storage.setItem(TOKEN_STORAGE_KEY, token);
    }
  } catch {
    // As above: an unavailable store costs persistence, not function.
  }
}

export function createApi(options: ApiOptions = {}): Api {
  const baseUrl = options.baseUrl ?? window.location.origin;
  const storage = options.storage ?? window.sessionStorage;
  const build = options.createClient ?? ((clientOptions) => new MockulusClient(clientOptions));

  const clientFor = (token: string | undefined): MockulusClient =>
    // The header is the SDK's to write. Handing it the token as an option
    // rather than assembling `Authorization: Token …` here keeps one spelling
    // of the scheme in the workspace, and it is the SDK's spelling because the
    // SDK is what the contract is written against.
    build(token === undefined ? { baseUrl } : { baseUrl, token });

  // Read once into an ordinary binding and used twice, rather than reading the
  // state back to seed the client. The two are kept in step by `submitToken`
  // and `clearToken`, which is a stronger guarantee than a derived would give —
  // the client is rebuilt exactly where the token changes and nowhere else.
  const storedToken = readStoredToken(storage);
  let token = $state(storedToken);
  let client = $state(clientFor(storedToken));
  let requested = $state(false);

  /**
   * The calls that were refused and want re-running. There is a list rather
   * than a single callback because a page can have several loads in flight —
   * the shell's and the view's — and every one of them 401s at once when the
   * deployment is token-protected; retrying only the last would leave the rest
   * showing an error the user already fixed.
   */
  let pending: (() => void)[] = [];

  return {
    get client() {
      return client;
    },
    get tokenRequested() {
      return requested;
    },
    get hasToken() {
      return token !== undefined;
    },
    requestToken(retry?: () => void) {
      // Deduplicated by identity: a resource queues its own bound reload, so a
      // second 401 from the same resource is the same function and must not
      // produce a second request when the token arrives.
      if (retry && !pending.includes(retry)) {
        pending.push(retry);
      }
      requested = true;
    },
    submitToken(value: string) {
      const trimmed = value.trim();
      if (trimmed === '') {
        return;
      }
      token = trimmed;
      writeStoredToken(storage, trimmed);
      client = clientFor(trimmed);
      requested = false;

      // Taken and cleared before running, so that a retry which 401s again —
      // the wrong token was pasted — queues itself afresh rather than joining a
      // list that is still being iterated.
      const retries = pending;
      pending = [];
      for (const retry of retries) {
        retry();
      }
    },
    dismissTokenRequest() {
      requested = false;
      pending = [];
    },
    clearToken() {
      token = undefined;
      writeStoredToken(storage, undefined);
      client = clientFor(undefined);
    },
  };
}

const API_KEY = Symbol('mockulus.api');

export function setApi(api: Api): void {
  setContext(API_KEY, api);
}

export function getApi(): Api {
  const api = getContext<Api | undefined>(API_KEY);
  if (!api) {
    throw new Error('mockulus ui: no api in context; mount this view inside App');
  }
  return api;
}
