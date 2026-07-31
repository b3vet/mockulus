// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient } from '@mockulus/admin-sdk';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createApi, type Api } from './api.svelte';
import { createResource } from './resource.svelte';
import { adminError, fakeClient } from './testing';

/** Lets a resolution settle without waiting on a timer. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe('createResource', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  function apiOver(client: MockulusClient): Api {
    return createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  }

  it('reports loading, then the answer', async () => {
    const api = apiOver(fakeClient({}));
    const resource = createResource(api, () => Promise.resolve('answer'));

    expect(resource.loading).toBe(true);
    expect(resource.data).toBeUndefined();

    await settle();

    expect(resource.loading).toBe(false);
    expect(resource.data).toBe('answer');
    expect(resource.error).toBeUndefined();
  });

  it('reports a failure and holds no stale data beside it', async () => {
    let answer: () => Promise<string> = () => Promise.resolve('first');
    const api = apiOver(fakeClient({}));
    const resource = createResource(api, () => answer());
    await settle();
    expect(resource.data).toBe('first');

    answer = () => Promise.reject(new Error('boom'));
    resource.reload();
    await settle();

    expect(resource.data).toBeUndefined();
    expect(resource.error).toBeInstanceOf(Error);
  });

  it('opens the token sheet on a 401 and re-runs the call once a token arrives', async () => {
    let authorized = false;
    const api = apiOver(fakeClient({}));
    const resource = createResource(api, () => {
      if (!authorized) {
        return Promise.reject(adminError(401, [{ code: 10, title: 'Unauthorized' }]));
      }
      return Promise.resolve('mappings');
    });

    await settle();
    expect(api.tokenRequested).toBe(true);
    expect(resource.data).toBeUndefined();

    authorized = true;
    api.submitToken('s3cret');
    await settle();

    expect(api.tokenRequested).toBe(false);
    expect(resource.data).toBe('mappings');
    expect(resource.error).toBeUndefined();
  });

  it('does not ask for a token when the failure is not a 401', async () => {
    const api = apiOver(fakeClient({}));
    createResource(api, () =>
      Promise.reject(adminError(500, [{ code: 1010, title: 'Journal disabled' }])),
    );

    await settle();

    expect(api.tokenRequested).toBe(false);
  });

  it('lets the newest run win, so a superseded answer cannot overwrite it', async () => {
    const resolvers: ((value: string) => void)[] = [];
    const api = apiOver(fakeClient({}));
    const resource = createResource(
      api,
      () => new Promise<string>((resolve) => resolvers.push(resolve)),
    );

    resource.reload();
    expect(resolvers).toHaveLength(2);

    // The first request answers last, which is what a slow page and a fast one
    // look like when a filter is retyped.
    resolvers[1]?.('second');
    resolvers[0]?.('first');
    await settle();

    expect(resource.data).toBe('second');
  });

  it('reads the client through the api on every run, so a retry uses the new one', async () => {
    // Each client the api builds is told which generation it belongs to, and the
    // resource reports back whichever one it actually called. Identity would
    // have been the obvious assertion and is not available: `$state` proxies a
    // plain test double, so the object the resource sees is not the object the
    // test handed over. A real SDK client is a class instance and is not
    // proxied, which is why this only bites the fake.
    let generation = 'before';
    const api = createApi({
      baseUrl: 'http://mock.example',
      createClient: () => {
        const built = generation;
        return fakeClient({
          system: {
            version: () =>
              Promise.resolve({ version: built, guessedWireMockVersion: '3.x-subset' }),
          } as Partial<MockulusClient['system']>,
        });
      },
    });
    const resource = createResource(api, (client) => client.system.version());
    await settle();
    expect(resource.data?.version).toBe('before');

    generation = 'after';
    api.submitToken('s3cret');
    resource.reload();
    await settle();

    expect(resource.data?.version).toBe('after');
  });

  it('does not resolve into a superseded generation after an error', async () => {
    const api = apiOver(fakeClient({}));
    const load = vi
      .fn<() => Promise<string>>()
      .mockReturnValueOnce(Promise.reject(new Error('slow failure')))
      .mockReturnValueOnce(Promise.resolve('fresh'));
    const resource = createResource(api, load);

    resource.reload();
    await settle();

    expect(resource.data).toBe('fresh');
    expect(resource.error).toBeUndefined();
  });
});
