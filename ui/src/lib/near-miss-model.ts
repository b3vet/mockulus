// SPDX-License-Identifier: Apache-2.0
import type {
  DescribedRequest,
  Difference,
  LoggedRequest,
  NearMiss,
  StubMapping,
} from '@mockulus/admin-sdk';

/**
 * Reading a near-miss answer, and writing the request that asks for one. No
 * Svelte and no network, for the reason the journal's half is separate: the
 * interesting cases here are documents — a candidate whose `differences` came
 * back `null`, a header line somebody typed without a colon — and none of them
 * needs a component to exercise.
 *
 * The structured `differences` are the thing worth handling well. WireMock
 * reports its own diff as rendered prose on the serve event; mockulus reports
 * which criterion, what the stub asked for and what arrived, as data. A UI that
 * printed that back as JSON would be throwing away the whole difference.
 */

/** One request and the candidates the server ranked against it. */
export interface NearMissGroup {
  readonly request: LoggedRequest;
  readonly candidates: readonly NearMiss[];
}

/**
 * Groups a flat near-miss list back into one block per request.
 *
 * Every near-miss endpoint answers one flat list of request-and-candidate
 * pairings — the shape WireMock's clients deserialize — so the request is
 * repeated on each pairing and the reader would otherwise see it three times.
 *
 * Grouping is by **adjacency**, not by a key built out of the request's fields:
 * the server emits one contiguous block per request, and a keyed grouping would
 * be claiming the request identifies itself, which it does not.
 *
 * Two consecutive blocks can still be about identical requests — a test that
 * calls the same unmatched endpoint twice produces exactly that — and the
 * requests then compare equal down to the millisecond. What separates them is
 * the candidates: scoring visits each stub once per request, so a stub can
 * appear at most once within a block, and a repeat is the start of the next
 * one. Without that rule two identical unmatched requests would merge into one
 * block listing every stub twice, and the page would report half as many
 * unmatched requests as arrived.
 */
export function groupByRequest(nearMisses: readonly NearMiss[]): NearMissGroup[] {
  const groups: { request: LoggedRequest; candidates: NearMiss[] }[] = [];
  let previousKey: string | undefined;
  let seenStubs = new Set<string>();

  for (const nearMiss of nearMisses) {
    const key = JSON.stringify(nearMiss.request);
    const stubId = nearMiss.stubMapping?.id ?? nearMiss.stubMapping?.uuid;
    const current = groups[groups.length - 1];
    const repeatsStub = stubId !== undefined && seenStubs.has(stubId);

    if (current !== undefined && key === previousKey && !repeatsStub) {
      current.candidates.push(nearMiss);
      if (stubId !== undefined) {
        seenStubs.add(stubId);
      }
      continue;
    }

    groups.push({ request: nearMiss.request, candidates: [nearMiss] });
    previousKey = key;
    seenStubs = new Set(stubId === undefined ? [] : [stubId]);
  }

  return groups;
}

/**
 * How close a candidate came, as a percentage a reader can rank by sight.
 *
 * The server's `distance` is 0 for an exact match and 1 for nothing in common,
 * which is the right scale to compute in and the wrong one to read: "0.17" has
 * to be inverted in the reader's head every time. Rounded, because the third
 * decimal of a heuristic distance is not information — the ranking is
 * mockulus's own and no matching decision depends on it.
 */
export function matchPercent(distance: number): number {
  if (!Number.isFinite(distance)) {
    return 0;
  }
  const clamped = Math.min(Math.max(distance, 0), 1);
  return Math.round((1 - clamped) * 100);
}

/**
 * The criteria that did not line up. `null` is a real answer — an exact match
 * against a supplied pattern produces it on `/near-misses/request-pattern` —
 * and reads the same as none here.
 */
export function differencesOf(nearMiss: NearMiss): readonly Difference[] {
  return nearMiss.matchResult.differences ?? [];
}

/**
 * How a difference names the criterion it is about: the class, plus the header,
 * cookie or parameter name for the classes that have one. `method` and `url`
 * carry no name, so they read as themselves.
 */
export function differenceLabel(difference: Difference): string {
  const name = difference.name;
  return name === undefined || name === '' ? difference.kind : `${difference.kind} ${name}`;
}

/**
 * The candidate stub, when the endpoint pairs requests with stubs.
 *
 * Two of the three near-miss endpoints do; `/near-misses/request-pattern` pairs
 * a recorded request with the pattern that was supplied instead, and exactly one
 * of the two is ever present. The debugger uses only the two stub-side
 * endpoints, so this is `undefined` in practice — and is still checked, because
 * a view that assumed otherwise would render `undefined` into a link.
 */
export function candidateStub(nearMiss: NearMiss): StubMapping | undefined {
  return nearMiss.stubMapping;
}

/** What the compose form holds while it is being typed: text, all of it. */
export interface ComposeDraft {
  method: string;
  url: string;
  /** One `Name: value` per line. */
  headers: string;
  /** One `name: value` per line. */
  cookies: string;
  body: string;
}

/** A blank form. `GET /` is what the server reads an empty description as. */
export function emptyDraft(): ComposeDraft {
  return { method: 'GET', url: '/', headers: '', cookies: '', body: '' };
}

export type DraftResult =
  | { readonly ok: true; readonly request: DescribedRequest }
  | { readonly ok: false; readonly message: string };

/**
 * Parses the `Name: value` lines of a header or cookie box.
 *
 * Split on the **first** colon only: a `Referer: https://example.test/` has two
 * and the second belongs to the value. Blank lines are skipped so that a
 * trailing newline — which every text area collects — is not a problem to
 * report.
 */
export function parseNameValueLines(
  text: string,
  what: string,
): { ok: true; values: Record<string, string> } | { ok: false; message: string } {
  const values: Record<string, string> = {};

  for (const [index, line] of text.split('\n').entries()) {
    const trimmed = line.trim();
    if (trimmed === '') {
      continue;
    }
    const colon = trimmed.indexOf(':');
    if (colon <= 0) {
      return {
        ok: false,
        message: `Line ${index + 1} of the ${what} is not a \`Name: value\` pair: ${trimmed}`,
      };
    }
    const name = trimmed.slice(0, colon).trim();
    values[name] = trimmed.slice(colon + 1).trim();
  }

  return { ok: true, values };
}

/**
 * Turns a filled-in form into the document the endpoint takes, or says what is
 * wrong with it.
 *
 * Empty members are left out rather than sent empty. The contract closes this
 * object and the server fills in `GET` and `/` for itself, so an absent field
 * means "as a request with none of this", which is exactly what an empty box
 * means, while an empty string would be a header whose value is the empty
 * string.
 */
export function toDescribedRequest(draft: ComposeDraft): DraftResult {
  const method = draft.method.trim().toUpperCase();
  const url = draft.url.trim();

  if (method === '') {
    return { ok: false, message: 'A method is needed; the server reads an absent one as GET.' };
  }
  if (url === '') {
    return { ok: false, message: 'A URL is needed. It is a path and query, such as /api/orders.' };
  }
  if (!url.startsWith('/')) {
    // The endpoint takes the path and query a request would carry, and reads
    // the query string out of it. A value that is not a path would be scored
    // against the stubs as written and produce three distances nobody can act
    // on, which is worse than being told to add the slash.
    return { ok: false, message: `A URL starts at the root: write /${url} rather than ${url}.` };
  }

  const headers = parseNameValueLines(draft.headers, 'headers');
  if (!headers.ok) {
    return { ok: false, message: headers.message };
  }
  const cookies = parseNameValueLines(draft.cookies, 'cookies');
  if (!cookies.ok) {
    return { ok: false, message: cookies.message };
  }

  const request: DescribedRequest = { method, url };
  if (Object.keys(headers.values).length > 0) {
    request.headers = headers.values;
  }
  if (Object.keys(cookies.values).length > 0) {
    request.cookies = cookies.values;
  }
  if (draft.body !== '') {
    request.body = draft.body;
  }
  return { ok: true, request };
}

/**
 * Fills the compose form from a request that was recorded, which is how the
 * journal hands an unmatched entry to the debugger.
 *
 * A repeated header collapses to its first value, because a described request
 * carries one literal value per name. That loses something, and the form is
 * text the reader can correct — which is better than the alternative of
 * refusing to carry the entry across at all.
 */
export function draftFromLoggedRequest(request: LoggedRequest): ComposeDraft {
  const lines = (map: Readonly<Record<string, string | string[]>>): string =>
    Object.entries(map)
      .map(([name, value]) => `${name}: ${Array.isArray(value) ? (value[0] ?? '') : value}`)
      .sort((a, b) => a.localeCompare(b))
      .join('\n');

  return {
    method: request.method,
    url: request.url,
    headers: lines(request.headers),
    cookies: lines(request.cookies),
    body: request.body,
  };
}
