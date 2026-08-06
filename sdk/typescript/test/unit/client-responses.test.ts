// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { ErrorCode, isMockulusError, MockulusClient } from '../../src/index.js';
import { bodyless, errors, inOrder, json, stubFetch, type RecordedCall } from './stub-fetch.js';

/**
 * What the client reads back.
 *
 * Three of the shapes here are the ones a client written from a summary of this
 * API gets wrong, so each has a case of its own: the error envelope, which
 * carries *every* problem and not the first; the answers that have no body at
 * all, which `response.json()` throws on; and the bodyless 404, which is the
 * only not-found on this surface a `…OrNull` may flatten.
 */

const baseUrl = 'http://mock.test:9090';

/** A client answered by `handler`, and the log of what it asked for. */
function client(handler: (call: RecordedCall) => Response) {
  const stub = stubFetch(handler);
  return { calls: stub.calls, client: new MockulusClient({ baseUrl, fetch: stub.fetch }) };
}

describe('an error envelope', () => {
  it('becomes a MockulusError carrying every problem, not the first', async () => {
    const { client: c } = client(() =>
      errors(
        422,
        {
          code: ErrorCode.Regex,
          title: 'Invalid regular expression',
          detail: 'urlPattern does not compile',
          source: { pointer: '/request/urlPattern' },
        },
        {
          code: ErrorCode.UnsupportedFeature,
          title: 'Unsupported feature',
          detail: 'matchesXPath is not supported in mockulus v1 — see ROADMAP.md',
          source: { pointer: '/request/bodyPatterns/0/matchesXPath' },
        },
      ),
    );

    // A 422 that named two fields and reported one would send the reader back
    // for a second round trip, which is the thing the collect-all envelope
    // exists to prevent — so the client must not throw the second away either.
    const err = await c.mappings.create({}).catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);

    expect(err.status).toBe(422);
    expect(err.problems).toHaveLength(2);
    expect(err.code).toBe(ErrorCode.Regex);
    expect(err.has(ErrorCode.UnsupportedFeature)).toBe(true);
    expect(err.isUnsupportedFeature).toBe(true);
    expect(err.pointers()).toEqual(['/request/urlPattern', '/request/bodyPatterns/0/matchesXPath']);
    expect(err.method).toBe('POST');
    expect(err.path).toBe('/__admin/mappings');
    expect(err.message).toContain('/request/urlPattern');
  });

  it('reports the journal being off as the configuration answer it is', async () => {
    const { client: c } = client(() =>
      errors(500, {
        code: ErrorCode.JournalDisabled,
        title: 'Request journal disabled',
        detail: 'set journal_enabled to record and verify requests',
      }),
    );

    const err = await c.requests.list().catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.isJournalDisabled).toBe(true);
    expect(err.status).toBe(500);
  });

  it('reports a 401 whether or not it could read the envelope', async () => {
    const { client: c } = client(() => errors(401, { code: ErrorCode.Malformed, title: 'Error' }));
    const err = await c.mappings.list().catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.isUnauthorized).toBe(true);
  });

  it('carries the raw body of an answer that was never ours', async () => {
    // An ingress or a proxy in front of the deployment answers in its own
    // shape. Hiding that body would hide the whole diagnosis: the reader needs
    // to see that they did not reach mockulus at all.
    const { client: c } = client(
      () => new Response('<html>502 Bad Gateway</html>', { status: 502 }),
    );

    const err = await c.mappings.list().catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.problems).toEqual([]);
    expect(err.code).toBeUndefined();
    expect(err.body).toContain('502 Bad Gateway');
    expect(err.message).toContain('502 Bad Gateway');
  });
});

describe('an answer with no body', () => {
  it('is what the import, the settings write and the drain resolve on', async () => {
    // All three really do answer 200 with no body and no `Content-Type`. A
    // client that called `response.json()` here would throw on a call that
    // succeeded.
    const { client: c } = client(() => bodyless(200));
    await expect(c.mappings.import({ mappings: [] })).resolves.toBeUndefined();
    await expect(c.settings.update({ fixedDelay: 50 })).resolves.toBeUndefined();
    await expect(c.system.shutdown()).resolves.toBeUndefined();
  });

  it('is what a file upload answers, and it is a 201 rather than a 200', async () => {
    const { client: c } = client(() => bodyless(201));
    await expect(c.files.put('order-created.json', '{}')).resolves.toBeUndefined();
  });

  it('is what deleting a file answers, whether or not the name was stored', async () => {
    const { client: c } = client(() => bodyless(200));
    await expect(c.files.delete('never-uploaded.json')).resolves.toBeUndefined();
  });

  it('does not stop the acknowledgements that do carry `{}`', async () => {
    // Half of these endpoints answer the empty JSON object and half answer
    // nothing at all. Both have to resolve, because which is which is not
    // something a caller should have to know.
    const { client: c } = client(() => json(200, {}));
    await expect(c.mappings.reset()).resolves.toBeUndefined();
    await expect(c.mappings.save()).resolves.toBeUndefined();
    await expect(c.requests.clear()).resolves.toBeUndefined();
    await expect(c.scenarios.reset()).resolves.toBeUndefined();
    await expect(c.system.resetAll()).resolves.toBeUndefined();
  });
});

describe('the `…OrNull` variants', () => {
  const id = '9c47901d-6bd5-4b7a-8896-c0ac9b8d0b4e';

  it('answer null for the bodyless 404 an unknown id gets', async () => {
    const { client: c } = client(() => bodyless(404));
    await expect(c.mappings.getOrNull(id)).resolves.toBeNull();
    await expect(c.requests.getOrNull('2VZ8mQ0kZ9pQnR6b1cN3sT7yWxE')).resolves.toBeNull();
  });

  it('leave the throwing default alone', async () => {
    const { client: c } = client(() => bodyless(404));
    const err = await c.mappings.get(id).catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.status).toBe(404);
    expect(err.problems).toEqual([]);
  });

  it('still throw a 404 that carries an envelope', async () => {
    // An unsupported endpoint is also a 404, and answering `null` to it would
    // tell a caller "no such stub" when what happened is that they reached a
    // server which does not implement the route at all.
    const { client: c } = client(() =>
      errors(404, {
        code: ErrorCode.UnsupportedEndpoint,
        title: 'Unsupported endpoint',
        detail: 'see ROADMAP.md',
      }),
    );

    const err = await c.mappings.getOrNull(id).catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.isUnsupportedEndpoint).toBe(true);
  });

  it('still throw the 400 a segment that is not a UUID gets', async () => {
    // 404 says "no such stub" and invites the caller to believe it once
    // existed; 400 says this could never have named one. Flattening the second
    // into `null` would erase the difference.
    const { client: c } = client(() =>
      errors(400, {
        code: ErrorCode.Malformed,
        title: 'Error',
        detail: 'not-a-uuid is not a UUID',
      }),
    );

    const err = await c.mappings.getOrNull('not-a-uuid').catch((e: unknown) => e);
    if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
    expect(err.status).toBe(400);
  });

  it('do not swallow the journal being off', async () => {
    const { client: c } = client(() =>
      errors(500, { code: ErrorCode.JournalDisabled, title: 'Request journal disabled' }),
    );
    await expect(c.requests.getOrNull('2VZ8mQ0kZ9pQnR6b1cN3sT7yWxE')).rejects.toThrow(
      /Request journal disabled/,
    );
  });
});

describe('paginating the mappings', () => {
  /** A snapshot of `total` stubs, paged the way the server pages one. */
  function snapshot(total: number): (call: RecordedCall) => Response {
    const all = Array.from({ length: total }, (_, i) => ({
      id: `stub-${i}`,
      priority: 5,
      persistent: false,
    }));
    return (call) => {
      const limit = Number(call.url.searchParams.get('limit'));
      const offset = Number(call.url.searchParams.get('offset'));
      // `meta.total` is the whole snapshot rather than the page, which is what
      // the server answers and what makes it useless as a stopping condition
      // the moment anything else writes a stub.
      return json(200, { mappings: all.slice(offset, offset + limit), meta: { total } });
    };
  }

  it('walks every page and stops on the short one', async () => {
    const { client: c, calls } = client(snapshot(5));

    const seen: string[] = [];
    for await (const mapping of c.mappings.paginate({ pageSize: 2 })) {
      seen.push(String(mapping.id));
    }

    expect(seen).toEqual(['stub-0', 'stub-1', 'stub-2', 'stub-3', 'stub-4']);
    expect(calls.map((call) => call.url.searchParams.get('offset'))).toEqual(['0', '2', '4']);
  });

  it('needs one more page when the last one is exactly full', async () => {
    // Four stubs in pages of two: the page at offset 4 is empty, and asking for
    // it is the only way to learn that. Stopping at a full page instead would
    // be stopping on a guess.
    const { client: c, calls } = client(snapshot(4));

    const seen: string[] = [];
    for await (const mapping of c.mappings.paginate({ pageSize: 2 })) {
      seen.push(String(mapping.id));
    }

    expect(seen).toHaveLength(4);
    expect(calls).toHaveLength(3);
  });

  it('terminates on an empty snapshot after exactly one request', async () => {
    const { client: c, calls } = client(snapshot(0));

    const seen: string[] = [];
    for await (const mapping of c.mappings.paginate()) {
      seen.push(String(mapping.id));
    }

    expect(seen).toEqual([]);
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.searchParams.get('limit')).toBe('100');
  });

  it('refuses a page size that would never advance, before calling anything', async () => {
    const { client: c, calls } = client(snapshot(3));

    const seen: string[] = [];
    const walk = async () => {
      for await (const mapping of c.mappings.paginate({ pageSize: 0 })) {
        seen.push(String(mapping.id));
      }
    };

    // A zero page is not a smaller request. The server reads a limit it cannot
    // use as absent and answers with everything, so the loop would restart from
    // the top forever rather than fail.
    await expect(walk()).rejects.toThrow(TypeError);
    expect(calls).toHaveLength(0);
  });
});

describe('the envelopes', () => {
  it('come back as the server sent them rather than unwrapped', async () => {
    const { client: c } = client(
      inOrder(
        json(200, { mappings: [{ id: 'a', priority: 5, persistent: false }], meta: { total: 7 } }),
        json(200, { count: 3, requestJournalDisabled: false }),
        json(200, { settings: { fixedDelay: 50 } }),
      ),
    );

    // `meta.total` is the whole point of the listing envelope: a client handed
    // a bare array cannot tell a page from the set.
    const page = await c.mappings.list({ limit: 1 });
    expect(page.meta.total).toBe(7);
    expect(page.mappings).toHaveLength(1);

    const count = await c.requests.count({ method: 'GET' });
    expect(count.count).toBe(3);

    const settings = await c.settings.get();
    expect(settings.settings.fixedDelay).toBe(50);
  });

  it('are not in the way where the server does not use one', async () => {
    const { client: c } = client(inOrder(json(200, ['a.json', 'fixtures/b.bin'])));
    // The file listing is the one bare array on this surface.
    await expect(c.files.list()).resolves.toEqual(['a.json', 'fixtures/b.bin']);
  });

  it('do not apply to a file download, which is bytes', async () => {
    const { client: c } = client(
      () =>
        new Response(new Uint8Array([0x00, 0x7b, 0xff]), {
          status: 200,
          headers: { 'Content-Type': 'application/octet-stream' },
        }),
    );

    const body = await c.files.get('fixtures/blob.bin');
    expect(body).toBeInstanceOf(ArrayBuffer);
    expect(Array.from(new Uint8Array(body))).toEqual([0x00, 0x7b, 0xff]);
  });
});
