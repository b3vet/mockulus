// SPDX-License-Identifier: Apache-2.0

/**
 * The few things every resource namespace needs and none of them owns.
 *
 * Each item here exists because the alternative was the same three lines copied
 * into seven files, and a rule copied seven times is a rule that will be fixed
 * in six of them.
 */

import { isMockulusError } from '../errors.js';
import type { RequestOptions } from './transport.js';

/**
 * Options for a call that reads the request journal.
 *
 * `since` is honoured on **seven** endpoints — the journal listing, the
 * unmatched listing, the unmatched near-misses, all three criteria queries, and
 * `POST /__admin/near-misses/request-pattern` — rather than only on the plain
 * listing, which is what a client would guess. It is refused rather than
 * dropped when it is not a timestamp, so a windowed verification can never
 * silently widen to the whole journal, and the value this SDK sends is always
 * one the server parses.
 */
export interface JournalWindowOptions extends RequestOptions {
  /**
   * Restricts the query to entries recorded at or after this moment. A `Date`
   * is rendered as RFC 3339 UTC; a string is sent as written, for a caller that
   * already holds a timestamp in the shape it wants.
   */
  since?: string | Date;
}

/** Renders a `since` bound for the query string, or nothing when there is none. */
export function sinceParam(since: string | Date | undefined): string | undefined {
  if (since === undefined) return undefined;
  // `toISOString` produces the UTC spelling with milliseconds, which RFC 3339
  // admits and the server's parser accepts. Converting here rather than at the
  // call sites is what keeps a `Date` from ever reaching `String()`, whose
  // output — `Wed Jul 29 2026 …` — is refused with a 422 rather than dropped.
  return typeof since === 'string' ? since : since.toISOString();
}

/**
 * Escapes a value being spliced into one path segment.
 *
 * Stub ids and serve-event ids are alphanumeric in practice, but a scenario
 * name is whatever a stub called it. An unescaped `?` or `#` would end the path
 * and turn the rest of the name into a query or a fragment, which reaches the
 * server as a call on a different route rather than as a bad name.
 */
export function encodeSegment(value: string): string {
  return encodeURIComponent(value);
}

/**
 * Escapes a file name, which may legitimately contain slashes.
 *
 * `fixtures/large.bin` is one name and not a nested route — the store holds
 * names rather than paths — but the route that carries it is a trailing
 * wildcard, so the slashes stay slashes on the wire and only the characters
 * that would change the meaning of the URL are escaped.
 */
export function encodeFileName(name: string): string {
  return name.split('/').map(encodeURIComponent).join('/');
}

/**
 * Runs a call and turns the **bodyless** 404 into `null`.
 *
 * The narrowness is the point. Three admin routes answer an unknown id with a
 * bare 404 carrying no body at all, because that is the not-found a WireMock
 * client library already handles; there the status is the whole message and a
 * thrown error tells the caller nothing they did not already know. Every other
 * 404 on this surface carries the error envelope — an unsupported endpoint is
 * code 1001, an unknown scenario is 1031, an unknown file name is 10 — and each
 * of those says something worth reading, so it is left to throw.
 *
 * That is why the test is `problems.length === 0` and not `status === 404`. A
 * client that mapped every 404 to `null` would answer "no such stub" to a
 * caller who had in fact reached a server that does not implement the route.
 */
export async function nullOnBodylessNotFound<T>(call: () => Promise<T>): Promise<T | null> {
  try {
    return await call();
  } catch (err) {
    if (isMockulusError(err) && err.status === 404 && err.problems.length === 0) {
      return null;
    }
    throw err;
  }
}
