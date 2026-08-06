// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';

import { Transport } from '../../src/client/transport.js';
import { MockulusError } from '../../src/errors.js';
import { ErrorCode } from '../../src/codes.js';

/**
 * A fetch that records what it was called with and answers what it was told.
 *
 * The answer is built per call rather than shared, because a Response body can
 * only be read once — a single instance handed back twice fails on the second
 * call with an error about the body rather than about the test.
 */
function recordingFetch(make: () => Response) {
  const calls: Array<{ url: string; init: RequestInit | undefined }> = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return make();
  }) as unknown as typeof globalThis.fetch;
  return { fn, calls };
}

const ok = () => new Response('{"ok":true}', { status: 200 });

describe('URL building', () => {
  it('joins the base and path without doubling or dropping a slash', async () => {
    for (const base of ['http://h:9090', 'http://h:9090/']) {
      const { fn, calls } = recordingFetch(ok);
      await new Transport({ baseUrl: base, fetch: fn }).send({
        method: 'GET',
        path: '/__admin/mappings',
      });
      expect(calls[0]?.url).toBe('http://h:9090/__admin/mappings');
    }
  });

  // A deployment behind an ingress may mount the admin API under a prefix, and
  // dropping it would send every call to a path that does not exist.
  it('keeps a path prefix on the base URL', async () => {
    const { fn, calls } = recordingFetch(ok);
    await new Transport({ baseUrl: 'http://h/mock-1', fetch: fn }).send({
      method: 'GET',
      path: '/__admin/mappings',
    });
    expect(calls[0]?.url).toBe('http://h/mock-1/__admin/mappings');
  });

  it('encodes query values and omits the undefined ones', async () => {
    const { fn, calls } = recordingFetch(ok);
    await new Transport({ baseUrl: 'http://h', fetch: fn }).send({
      method: 'GET',
      path: '/__admin/requests',
      query: { limit: 10, since: '2026-01-01T00:00:00Z', offset: undefined },
    });
    const url = new URL(calls[0]!.url);
    expect(url.searchParams.get('limit')).toBe('10');
    expect(url.searchParams.get('since')).toBe('2026-01-01T00:00:00Z');
    // An absent parameter must be absent rather than the string "undefined",
    // which several of these endpoints would read as a value and refuse.
    expect(url.searchParams.has('offset')).toBe(false);
  });
});

describe('the admin token', () => {
  it('is sent as Token, which is WireMock spelling and not Bearer', async () => {
    const { fn, calls } = recordingFetch(ok);
    await new Transport({ baseUrl: 'http://h', token: 's3cret', fetch: fn }).send({
      method: 'GET',
      path: '/__admin/mappings',
    });
    const headers = calls[0]?.init?.headers as Record<string, string>;
    expect(headers['Authorization']).toBe('Token s3cret');
  });

  it('is absent when no token is configured', async () => {
    const { fn, calls } = recordingFetch(ok);
    await new Transport({ baseUrl: 'http://h', fetch: fn }).send({
      method: 'GET',
      path: '/__admin/mappings',
    });
    const headers = calls[0]?.init?.headers as Record<string, string>;
    expect(headers['Authorization']).toBeUndefined();
  });
});

describe('error mapping', () => {
  it('carries every problem the envelope reported, not the first', async () => {
    const envelope = {
      errors: [
        {
          code: 1000,
          source: { pointer: '/request/bodyPatterns/0/equalToXml' },
          title: 'Unsupported feature',
          detail: 'equalToXml is not supported',
        },
        {
          code: 1000,
          source: { pointer: '/postServeActions' },
          title: 'Unsupported feature',
          detail: 'postServeActions is not supported',
        },
      ],
    };
    const { fn } = recordingFetch(
      () =>
        new Response(JSON.stringify(envelope), {
          status: 422,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    const transport = new Transport({ baseUrl: 'http://h', fetch: fn });

    const err = await transport
      .send({ method: 'POST', path: '/__admin/mappings', body: {} })
      .catch((e: unknown) => e);

    expect(err).toBeInstanceOf(MockulusError);
    const mockulusError = err as MockulusError;
    expect(mockulusError.status).toBe(422);
    // The whole list. Collecting every problem is what makes a rejected
    // mapping one round trip on the server, and surfacing one would hand that
    // back a field at a time.
    expect(mockulusError.problems).toHaveLength(2);
    expect(mockulusError.pointers()).toEqual([
      '/request/bodyPatterns/0/equalToXml',
      '/postServeActions',
    ]);
    expect(mockulusError.isUnsupportedFeature).toBe(true);
    // And the message names them, so a thrown error read in a log is actionable
    // without anyone destructuring it.
    expect(mockulusError.message).toContain('/postServeActions');
  });

  it('recognises the journal-disabled answer, which is the default configuration', async () => {
    const { fn } = recordingFetch(
      () =>
        new Response(JSON.stringify({ errors: [{ code: 1010, title: 'Journal disabled' }] }), {
          status: 500,
        }),
    );
    const err = (await new Transport({ baseUrl: 'http://h', fetch: fn })
      .send({ method: 'GET', path: '/__admin/requests' })
      .catch((e: unknown) => e)) as MockulusError;

    expect(err.isJournalDisabled).toBe(true);
    expect(err.has(ErrorCode.JournalDisabled)).toBe(true);
    expect(err.isStoreUnavailable).toBe(false);
  });

  // Several endpoints answer a bare 404 with no body at all, and an answer that
  // is not ours — an ingress error page — carries something that is not an
  // envelope. Neither may produce an error that says nothing.
  it('survives an answer that carries no envelope', async () => {
    const { fn } = recordingFetch(() => new Response('', { status: 404 }));
    const bodyless = (await new Transport({ baseUrl: 'http://h', fetch: fn })
      .send({ method: 'GET', path: '/__admin/mappings/x' })
      .catch((e: unknown) => e)) as MockulusError;
    expect(bodyless.status).toBe(404);
    expect(bodyless.problems).toEqual([]);
    expect(bodyless.code).toBeUndefined();
    expect(bodyless.message).toContain('no error body');

    const { fn: html } = recordingFetch(
      () => new Response('<html>502 Bad Gateway</html>', { status: 502 }),
    );
    const proxied = (await new Transport({ baseUrl: 'http://h', fetch: html })
      .send({ method: 'GET', path: '/__admin/mappings' })
      .catch((e: unknown) => e)) as MockulusError;
    // The raw body is kept, because for an answer that is not ours it is the
    // whole diagnosis.
    expect(proxied.message).toContain('502 Bad Gateway');
  });
});

describe('response reading', () => {
  it('treats an empty 200 as no value rather than a parse error', async () => {
    const { fn } = recordingFetch(() => new Response('', { status: 200 }));
    const result = await new Transport({ baseUrl: 'http://h', fetch: fn }).send({
      method: 'POST',
      path: '/__admin/settings',
      body: { fixedDelay: 0 },
    });
    expect(result).toBeUndefined();
  });

  it('returns bytes when asked, for the files API', async () => {
    const { fn } = recordingFetch(() => new Response(new Uint8Array([1, 2, 3]), { status: 200 }));
    const bytes = await new Transport({ baseUrl: 'http://h', fetch: fn }).send<ArrayBuffer>({
      method: 'GET',
      path: '/__admin/files/x.bin',
      accept: 'bytes',
    });
    expect(new Uint8Array(bytes)).toEqual(new Uint8Array([1, 2, 3]));
  });
});

describe('cancellation', () => {
  it('passes the caller signal through when no timeout is configured', async () => {
    const { fn, calls } = recordingFetch(ok);
    const controller = new AbortController();
    await new Transport({ baseUrl: 'http://h', fetch: fn }).send({
      method: 'GET',
      path: '/__admin/mappings',
      signal: controller.signal,
    });
    expect(calls[0]?.init?.signal).toBe(controller.signal);
  });

  it('aborts on the caller signal even when a timeout is also configured', async () => {
    const controller = new AbortController();
    const transport = new Transport({ baseUrl: 'http://h', timeoutMs: 60_000, fetch: hang });
    const inFlight = transport.send({
      method: 'GET',
      path: '/__admin/mappings',
      signal: controller.signal,
    });
    controller.abort(new Error('caller changed its mind'));
    await expect(inFlight).rejects.toThrow('caller changed its mind');
  });

  it('aborts on the timeout when the caller never does', async () => {
    const transport = new Transport({ baseUrl: 'http://h', timeoutMs: 10, fetch: hang });
    await expect(
      transport.send({
        method: 'GET',
        path: '/__admin/mappings',
        signal: new AbortController().signal,
      }),
    ).rejects.toThrow();
  });

  // A composed signal keeps listeners on both halves. Left attached, the
  // caller's long-lived signal accumulates one listener per request and Node
  // warns about a leak somewhere far from the cause.
  it('detaches its listeners once the call is done', async () => {
    const { fn } = recordingFetch(ok);
    const controller = new AbortController();
    const transport = new Transport({ baseUrl: 'http://h', timeoutMs: 60_000, fetch: fn });
    for (let i = 0; i < 25; i++) {
      await transport.send({ method: 'GET', path: '/__admin/mappings', signal: controller.signal });
    }
    // Node exposes the count; a browser AbortSignal does not, so the assertion
    // is guarded rather than skipped outright.
    const listeners = (controller.signal as unknown as { listenerCount?: (t: string) => number })
      .listenerCount;
    if (typeof listeners === 'function') {
      expect(listeners.call(controller.signal, 'abort')).toBeLessThan(5);
    }
  });
});

/** A fetch that never settles until its signal aborts. */
const hang = (async (_input: RequestInfo | URL, init?: RequestInit) =>
  new Promise<Response>((_resolve, reject) => {
    init?.signal?.addEventListener('abort', () => reject(init.signal?.reason), { once: true });
  })) as unknown as typeof globalThis.fetch;
