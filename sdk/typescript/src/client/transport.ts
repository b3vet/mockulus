// SPDX-License-Identifier: Apache-2.0

import { MockulusError, type MockulusProblem } from '../errors.js';

/** How a {@link MockulusClient} is configured. */
export interface MockulusClientOptions {
  /**
   * Where the admin API is. Either port works — the admin surface is on the
   * mock port too unless `admin_on_mock_port` was turned off — and a path
   * prefix is honoured, for a deployment behind an ingress that mounts it
   * somewhere other than the root.
   */
  baseUrl: string;
  /**
   * The `admin_auth_token`, when one is configured. It is sent as
   * `Authorization: Token <token>`; the scheme word is `Token` and not
   * `Bearer`, which is WireMock's spelling and therefore ours.
   */
  token?: string;
  /**
   * A `fetch` to use instead of the platform's. Supplied for tests and for
   * runtimes that want their own agent or interceptors; nothing here depends on
   * anything beyond the standard signature.
   */
  fetch?: typeof globalThis.fetch;
  /**
   * A per-request timeout in milliseconds. Absent means no timeout, which is
   * the right default for a library: a caller that wants one usually wants it
   * to be theirs, and `signal` takes an `AbortSignal` for exactly that.
   */
  timeoutMs?: number;
  /** Headers added to every request. A per-call header of the same name wins. */
  headers?: Record<string, string>;
}

/** Options every call accepts. */
export interface RequestOptions {
  /** Cancels the call. Composed with `timeoutMs` when both are given. */
  signal?: AbortSignal;
  /** Extra headers for this call alone. */
  headers?: Record<string, string>;
}

/** What a request wants doing, before any of it is turned into HTTP. */
export interface Call extends RequestOptions {
  method: string;
  path: string;
  query?: Record<string, string | number | boolean | undefined>;
  /** Serialized as JSON unless it is already a string or bytes. */
  body?: unknown;
  /** Overrides the JSON content type, for the files API. */
  contentType?: string;
  /** How to read a 2xx body. `none` skips reading it at all. */
  accept?: 'json' | 'text' | 'bytes' | 'none';
}

/**
 * Transport is everything the resource namespaces share: URL building, the
 * token header, JSON encoding, error mapping and the timeout.
 *
 * It is separate from the client so that the namespaces depend on a small
 * surface rather than on each other, and so that this — the part where a
 * mistake is silent — can be tested on its own.
 */
export class Transport {
  private readonly baseUrl: string;
  private readonly token: string | undefined;
  private readonly doFetch: typeof globalThis.fetch;
  private readonly timeoutMs: number | undefined;
  private readonly headers: Record<string, string>;

  constructor(options: MockulusClientOptions) {
    if (!options.baseUrl) {
      throw new TypeError('baseUrl is required');
    }
    // Stored without a trailing slash so joining a path is unambiguous. A
    // base carrying a path prefix keeps it, for a deployment behind an ingress.
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.token = options.token;
    this.timeoutMs = options.timeoutMs;
    this.headers = options.headers ?? {};

    const chosen = options.fetch ?? globalThis.fetch;
    if (typeof chosen !== 'function') {
      throw new TypeError(
        'no fetch is available; pass one as `fetch` or run on a platform that has it',
      );
    }
    // Bound because a bare `globalThis.fetch` called as a method of `this`
    // throws an illegal-invocation TypeError in browsers.
    this.doFetch = chosen.bind(globalThis);
  }

  url(path: string, query?: Call['query']): string {
    const url = new URL(this.baseUrl + path);
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined) {
        url.searchParams.set(key, String(value));
      }
    }
    return url.toString();
  }

  async send<T>(call: Call): Promise<T> {
    const headers: Record<string, string> = { ...this.headers, ...call.headers };
    if (this.token) {
      headers['Authorization'] = `Token ${this.token}`;
    }

    let body: BodyInit | undefined;
    if (call.body !== undefined) {
      if (typeof call.body === 'string' || call.body instanceof Uint8Array) {
        body = call.body as BodyInit;
        if (call.contentType) headers['Content-Type'] = call.contentType;
      } else {
        body = JSON.stringify(call.body);
        headers['Content-Type'] = call.contentType ?? 'application/json';
      }
    }

    const { signal, dispose } = this.signalFor(call.signal);
    let response: Response;
    try {
      const init: RequestInit = { method: call.method, headers };
      if (body !== undefined) init.body = body;
      if (signal) init.signal = signal;
      response = await this.doFetch(this.url(call.path, call.query), init);
    } finally {
      dispose();
    }

    if (!response.ok) {
      throw await this.errorFor(response, call);
    }

    switch (call.accept) {
      case 'none':
        // The body is drained rather than ignored: an unread body keeps a
        // connection out of the pool on several runtimes, and several of these
        // endpoints answer 200 with nothing in it.
        await response.arrayBuffer();
        return undefined as T;
      case 'text':
        return (await response.text()) as T;
      case 'bytes':
        return (await response.arrayBuffer()) as T;
      default:
        return (await this.json<T>(response)) as T;
    }
  }

  /** Reads a JSON body, tolerating the endpoints that answer 200 with nothing. */
  private async json<T>(response: Response): Promise<T | undefined> {
    const text = await response.text();
    if (text.trim() === '') return undefined;
    return JSON.parse(text) as T;
  }

  private async errorFor(response: Response, call: Call): Promise<MockulusError> {
    const text = await response.text().catch(() => '');
    let problems: MockulusProblem[] | undefined;
    if (text.trim() !== '') {
      try {
        const parsed: unknown = JSON.parse(text);
        if (parsed && typeof parsed === 'object' && Array.isArray((parsed as never)['errors'])) {
          problems = (parsed as { errors: MockulusProblem[] }).errors;
        }
      } catch {
        // Not an envelope. The raw text is carried instead, which matters for
        // the answers that are not ours at all — an ingress 502, a proxy's
        // error page — where hiding the body would hide the whole diagnosis.
      }
    }
    const init: {
      status: number;
      method: string;
      path: string;
      problems?: MockulusProblem[];
      body?: string;
    } = { status: response.status, method: call.method, path: call.path };
    if (problems) init.problems = problems;
    if (text) init.body = text;
    return new MockulusError(init);
  }

  /**
   * Composes the caller's signal with the configured timeout.
   *
   * Both have to work at once: a caller that passes a signal should not lose
   * the timeout, and a timeout should not outlive the call it was created for —
   * an uncleared timer keeps a Node process alive after the work is done, which
   * is how a library makes someone's test suite hang.
   */
  private signalFor(caller?: AbortSignal): {
    signal: AbortSignal | undefined;
    dispose: () => void;
  } {
    if (this.timeoutMs === undefined) {
      return { signal: caller, dispose: () => {} };
    }
    const timeout = AbortSignal.timeout(this.timeoutMs);
    if (!caller) {
      return { signal: timeout, dispose: () => {} };
    }
    const controller = new AbortController();
    const abort = (reason: unknown) => controller.abort(reason);
    const onCaller = () => abort(caller.reason);
    const onTimeout = () => abort(timeout.reason);
    caller.addEventListener('abort', onCaller, { once: true });
    timeout.addEventListener('abort', onTimeout, { once: true });
    return {
      signal: controller.signal,
      dispose: () => {
        caller.removeEventListener('abort', onCaller);
        timeout.removeEventListener('abort', onTimeout);
      },
    };
  }
}
