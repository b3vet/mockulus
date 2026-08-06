// SPDX-License-Identifier: Apache-2.0

/**
 * A `fetch` the unit lane hands the client instead of the platform's.
 *
 * The client takes one through `MockulusClientOptions.fetch`, which is the seam
 * that lets everything below the wire — URL and query construction, the token
 * header, encoding, error mapping, the handling of an answer with no body — be
 * tested without a server, a port or an ordering constraint. The integration
 * lane covers the other half: that the server really does answer the way these
 * cases assume.
 *
 * Two properties are deliberate. Calls are recorded in full, because half of
 * what is worth asserting here is what went out rather than what came back. And
 * a call the handler was not expecting **fails** rather than answering a
 * default: a client that makes one more request than it should is exactly the
 * defect a paginating loop has, and a stub that shrugged at an extra call would
 * be blind to it.
 */

/** One call the client made, as it reached `fetch`. */
export interface RecordedCall {
  method: string;
  /** Parsed, so a case can assert on the path and the parameters separately. */
  url: URL;
  headers: Headers;
  body: string | Uint8Array | undefined;
}

/** A stub `fetch` and the log of what has been asked of it. */
export interface StubFetch {
  calls: RecordedCall[];
  fetch: typeof globalThis.fetch;
}

/** Builds a stub `fetch` that answers each call from `handler`. */
export function stubFetch(handler: (call: RecordedCall) => Response): StubFetch {
  const calls: RecordedCall[] = [];
  const fetch: typeof globalThis.fetch = (input, init) => {
    const call: RecordedCall = {
      method: init?.method ?? 'GET',
      url: new URL(String(input)),
      // Normalized through `Headers` so a case can look one up without knowing
      // which spelling the transport chose.
      headers: new Headers(init?.headers),
      body: init?.body as string | Uint8Array | undefined,
    };
    calls.push(call);
    return Promise.resolve(handler(call));
  };
  return { calls, fetch };
}

/**
 * Answers the given responses in order, and fails on a call past the end.
 *
 * The failure is the point: it turns "the client made three requests where two
 * were expected" from a silent pass into a rejected call.
 */
export function inOrder(...responses: Response[]): (call: RecordedCall) => Response {
  let next = 0;
  return (call) => {
    const response = responses[next++];
    if (!response) {
      throw new Error(
        `unexpected call ${next} to ${call.method} ${call.url.pathname}${call.url.search}: ` +
          `only ${responses.length} were expected`,
      );
    }
    return response;
  };
}

/** A JSON answer, as the admin API's handlers write one. */
export function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * An answer with **no body and no `Content-Type`**, which is what several of
 * these endpoints really send: an unknown stub or serve-event id, an import, a
 * settings write, a file upload and a file delete.
 */
export function bodyless(status: number): Response {
  return new Response(null, { status });
}

/** The error envelope every refusal except the bodyless 404s carries. */
export function errors(
  status: number,
  ...problems: { code: number; title?: string; detail?: string; source?: { pointer: string } }[]
): Response {
  return json(status, { errors: problems });
}
