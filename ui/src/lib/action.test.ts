// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient } from '@mockulus/admin-sdk';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createAction } from './action.svelte';
import { createApi, type Api } from './api.svelte';
import { adminError, fakeClient } from './testing';

/** Lets a resolution settle without waiting on a timer. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe('createAction', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  function apiOver(client: MockulusClient = fakeClient({})): Api {
    return createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  }

  it('reports pending, then hands the answer to the success callback', async () => {
    const onsuccess = vi.fn();
    const action = createAction(
      apiOver(),
      (_client, name: string) => Promise.resolve(`${name}!`),
      onsuccess,
    );

    action.run('written');
    expect(action.pending).toBe(true);

    await settle();

    expect(action.pending).toBe(false);
    expect(action.error).toBeUndefined();
    expect(onsuccess).toHaveBeenCalledWith('written!');
  });

  it('keeps a failure and does not report success', async () => {
    const onsuccess = vi.fn();
    const action = createAction(apiOver(), () => Promise.reject(new Error('boom')), onsuccess);

    action.run();
    await settle();

    expect(action.error).toBeInstanceOf(Error);
    expect(onsuccess).not.toHaveBeenCalled();
  });

  it('opens the token sheet on a 401 and re-sends the same arguments once a token arrives', async () => {
    // The property that matters for a write: a Save refused for want of a token
    // must not lose the document it was saving.
    let authorized = false;
    const sent: string[] = [];
    const api = apiOver();
    const action = createAction(api, (_client, document: string) => {
      if (!authorized) {
        return Promise.reject(adminError(401, [{ code: 10, title: 'Unauthorized' }]));
      }
      sent.push(document);
      return Promise.resolve(document);
    });

    action.run('the draft');
    await settle();
    expect(api.tokenRequested).toBe(true);
    expect(sent).toEqual([]);

    authorized = true;
    api.submitToken('s3cret');
    await settle();

    expect(api.tokenRequested).toBe(false);
    expect(sent).toEqual(['the draft']);
    expect(action.error).toBeUndefined();
  });

  it('queues one retry however many times it was refused', async () => {
    // The api dedupes queued retries by function identity, which only works
    // because the retry is a stable closure over the last arguments rather than
    // a fresh one per call. Two refused saves must not become two writes.
    let authorized = false;
    let calls = 0;
    const api = apiOver();
    const action = createAction(api, (_client, document: string) => {
      if (!authorized) {
        return Promise.reject(adminError(401, [{ code: 10, title: 'Unauthorized' }]));
      }
      calls += 1;
      return Promise.resolve(document);
    });

    action.run('first');
    await settle();
    action.run('second');
    await settle();

    authorized = true;
    api.submitToken('s3cret');
    await settle();

    expect(calls).toBe(1);
  });

  it('uses the client the token rebuilt rather than the one that was refused', async () => {
    // The token each attempt travelled with, observed from inside the client the
    // api handed over. A retry that reused the captured client would repeat the
    // 401 that queued it, and the second entry here is what proves it does not.
    const carried: (string | undefined)[] = [];
    const api = createApi({
      baseUrl: 'http://mock.example',
      createClient: ({ token }) =>
        fakeClient({
          mappings: {
            delete: async () => {
              carried.push(token);
              if (token === undefined) {
                throw adminError(401, [{ code: 10, title: 'Unauthorized' }]);
              }
            },
          } as Partial<MockulusClient['mappings']>,
        }),
    });
    const action = createAction(api, (client, id: string) => client.mappings.delete(id));

    action.run('abc');
    await settle();
    api.submitToken('s3cret');
    await settle();

    expect(carried).toEqual([undefined, 's3cret']);
    expect(action.error).toBeUndefined();
  });

  it('lets the last of two overlapping calls decide the state', async () => {
    const resolvers: ((value: string) => void)[] = [];
    const action = createAction(
      apiOver(),
      () => new Promise<string>((resolve) => resolvers.push(resolve)),
    );

    action.run();
    action.run();
    // The first call answers last, and must not clear a pending flag the second
    // one owns.
    resolvers[1]?.('second');
    await settle();
    expect(action.pending).toBe(false);

    resolvers[0]?.('first');
    await settle();
    expect(action.pending).toBe(false);
    expect(action.error).toBeUndefined();
  });

  it('forgets a failure when reset, so a view can leave the surface clean', async () => {
    const action = createAction(apiOver(), () => Promise.reject(new Error('boom')));

    action.run();
    await settle();
    expect(action.error).toBeInstanceOf(Error);

    action.reset();
    expect(action.error).toBeUndefined();
    expect(action.pending).toBe(false);
  });
});
