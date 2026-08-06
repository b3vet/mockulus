// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, MockulusClientOptions } from '@mockulus/admin-sdk';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createApi } from './api.svelte';
import { fakeClient } from './testing';

/**
 * The token rules the SOW is specific about, asserted one at a time.
 *
 * Three of them are security properties rather than behaviour a user would
 * notice — where the value is stored, where it is not, and that the header is
 * the SDK's to write — so each is checked directly instead of being inferred
 * from the flow working.
 */
describe('createApi', () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  /** Records every set of options a client was built with, in order. */
  function trackingApi(baseUrl = 'http://mock.example') {
    const built: MockulusClientOptions[] = [];
    const createClient = (options: MockulusClientOptions): MockulusClient => {
      built.push(options);
      return fakeClient({});
    };
    return { built, api: createApi({ baseUrl, createClient }) };
  }

  it('starts with no token, and builds a client that carries none', () => {
    const { api, built } = trackingApi();

    expect(api.hasToken).toBe(false);
    expect(api.tokenRequested).toBe(false);
    expect(built[0]).toEqual({ baseUrl: 'http://mock.example' });
  });

  it('never opens the sheet on its own — only a 401 does', () => {
    const { api } = trackingApi();

    expect(api.tokenRequested).toBe(false);
  });

  it('opens the sheet when a call reports a 401', () => {
    const { api } = trackingApi();

    api.requestToken();

    expect(api.tokenRequested).toBe(true);
  });

  it('hands the token to the SDK as an option rather than building a header', () => {
    const { api, built } = trackingApi();

    api.submitToken('s3cret');

    // The scheme word is `Token` and not `Bearer`, and that spelling lives in
    // the SDK. Asserting on the option is asserting that the UI has not made a
    // second, silently divergent copy of it.
    expect(built.at(-1)).toEqual({ baseUrl: 'http://mock.example', token: 's3cret' });
  });

  it('keeps the token in sessionStorage and nowhere else', () => {
    const api = createApi({
      baseUrl: 'http://mock.example',
      createClient: () => fakeClient({}),
    });

    api.submitToken('s3cret');

    expect(sessionStorage.getItem('mockulus.admin-token')).toBe('s3cret');
    expect(localStorage.length).toBe(0);
    expect(document.cookie).toBe('');
    expect(window.location.search).not.toContain('s3cret');
  });

  it('picks a stored token back up, so a reload does not ask again', () => {
    sessionStorage.setItem('mockulus.admin-token', 'from-storage');

    const built: MockulusClientOptions[] = [];
    const api = createApi({
      baseUrl: 'http://mock.example',
      createClient: (options) => {
        built.push(options);
        return fakeClient({});
      },
    });

    expect(api.hasToken).toBe(true);
    expect(built[0]).toEqual({ baseUrl: 'http://mock.example', token: 'from-storage' });
  });

  it('re-runs the refused work once a token arrives, with no reload', () => {
    const { api } = trackingApi();
    const retry = vi.fn();

    api.requestToken(retry);
    expect(retry).not.toHaveBeenCalled();

    api.submitToken('s3cret');

    expect(retry).toHaveBeenCalledTimes(1);
    expect(api.tokenRequested).toBe(false);
  });

  it('re-runs every refused call, not only the last one', () => {
    const { api } = trackingApi();
    const first = vi.fn();
    const second = vi.fn();

    api.requestToken(first);
    api.requestToken(second);
    api.submitToken('s3cret');

    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);
  });

  it('queues one retry per caller, however many times it reports a 401', () => {
    const { api } = trackingApi();
    const retry = vi.fn();

    api.requestToken(retry);
    api.requestToken(retry);
    api.submitToken('s3cret');

    expect(retry).toHaveBeenCalledTimes(1);
  });

  it('re-queues a retry that fails again, rather than dropping it', () => {
    const { api } = trackingApi();
    const retry = vi.fn(() => api.requestToken(retry));

    api.requestToken(retry);
    api.submitToken('wrong');
    expect(api.tokenRequested).toBe(true);

    api.submitToken('right');
    expect(retry).toHaveBeenCalledTimes(2);
  });

  it('ignores an empty submission instead of storing one', () => {
    const { api } = trackingApi();
    const retry = vi.fn();

    api.requestToken(retry);
    api.submitToken('   ');

    expect(api.hasToken).toBe(false);
    expect(api.tokenRequested).toBe(true);
    expect(retry).not.toHaveBeenCalled();
  });

  it('trims what was pasted, because a copied token often brings whitespace', () => {
    const { api, built } = trackingApi();

    api.submitToken('  s3cret\n');

    expect(built.at(-1)).toEqual({ baseUrl: 'http://mock.example', token: 's3cret' });
  });

  it('abandons the queued work when the sheet is dismissed', () => {
    const { api } = trackingApi();
    const retry = vi.fn();

    api.requestToken(retry);
    api.dismissTokenRequest();

    expect(api.tokenRequested).toBe(false);

    api.submitToken('s3cret');
    expect(retry).not.toHaveBeenCalled();
  });

  it('forgets the token on request, from storage as well as from the client', () => {
    const { api, built } = trackingApi();

    api.submitToken('s3cret');
    api.clearToken();

    expect(api.hasToken).toBe(false);
    expect(sessionStorage.getItem('mockulus.admin-token')).toBeNull();
    expect(built.at(-1)).toEqual({ baseUrl: 'http://mock.example' });
  });

  it('survives storage being unavailable, at the cost of persistence only', () => {
    const throwing: Storage = {
      get length() {
        return 0;
      },
      clear: () => {},
      key: () => null,
      getItem: () => {
        throw new Error('storage is disabled');
      },
      setItem: () => {
        throw new Error('storage is disabled');
      },
      removeItem: () => {
        throw new Error('storage is disabled');
      },
    };

    const api = createApi({
      baseUrl: 'http://mock.example',
      storage: throwing,
      createClient: () => fakeClient({}),
    });

    expect(() => api.submitToken('s3cret')).not.toThrow();
    expect(api.hasToken).toBe(true);
  });
});
