// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { candidateMapping, loggedRequest, nearMiss } from './testing';
import {
  differenceLabel,
  differencesOf,
  draftFromLoggedRequest,
  emptyDraft,
  groupByRequest,
  matchPercent,
  parseNameValueLines,
  toDescribedRequest,
} from './near-miss-model';

describe('grouping a near-miss answer', () => {
  it('folds the repeated request out of one block', () => {
    const request = loggedRequest({ url: '/api/orders' });
    const groups = groupByRequest([
      nearMiss({ request, stubMapping: candidateMapping(1) }),
      nearMiss({ request, stubMapping: candidateMapping(2) }),
      nearMiss({ request, stubMapping: candidateMapping(3) }),
    ]);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.candidates).toHaveLength(3);
    expect(groups[0]?.request.url).toBe('/api/orders');
  });

  it('starts a new block where the request changes', () => {
    const groups = groupByRequest([
      nearMiss({ request: loggedRequest({ url: '/one' }) }),
      nearMiss({ request: loggedRequest({ url: '/two' }) }),
    ]);

    expect(groups.map((group) => group.request.url)).toEqual(['/one', '/two']);
  });

  it('keeps two identical recorded requests apart, since a stub is scored once each', () => {
    // Two entries a retrying test produced: equal in every field down to the
    // millisecond, so nothing about the request separates them. What does is
    // the candidate — a stub appears at most once per request, so seeing it
    // again is the next block starting. Merging them would report one unmatched
    // request where two arrived, and list every stub twice.
    const request = loggedRequest({ url: '/api/orders' });
    const block = [
      nearMiss({ request, stubMapping: candidateMapping(1) }),
      nearMiss({ request, stubMapping: candidateMapping(2) }),
    ];

    const groups = groupByRequest([...block, ...block]);

    expect(groups).toHaveLength(2);
    expect(groups[0]?.candidates).toHaveLength(2);
    expect(groups[1]?.candidates).toHaveLength(2);
  });

  it('has nothing to group when nothing came close', () => {
    expect(groupByRequest([])).toEqual([]);
  });
});

describe('reading a candidate', () => {
  it('turns the distance into the direction a reader ranks by', () => {
    // The server computes in distance, where 0 is an exact match. A reader
    // ranks in closeness, and inverting it in their head every time is what
    // this exists to save.
    expect(matchPercent(0)).toBe(100);
    expect(matchPercent(1)).toBe(0);
    expect(matchPercent(0.25)).toBe(75);
  });

  it('refuses to render a distance outside the scale as a percentage outside it', () => {
    expect(matchPercent(-1)).toBe(100);
    expect(matchPercent(2)).toBe(0);
    expect(matchPercent(Number.NaN)).toBe(0);
  });

  it('reads a null difference list as no differences', () => {
    // `null` is a real answer: an exact match against a supplied pattern
    // produces it on /near-misses/request-pattern.
    expect(
      differencesOf(nearMiss({ matchResult: { distance: 0, differences: null } })),
    ).toHaveLength(0);
  });

  it('names a criterion by its class, and by its name where it has one', () => {
    expect(differenceLabel({ kind: 'header', name: 'Accept', expected: 'a', actual: 'b' })).toBe(
      'header Accept',
    );
    expect(differenceLabel({ kind: 'url', expected: '/a', actual: '/b' })).toBe('url');
    expect(differenceLabel({ kind: 'method', name: '', expected: 'GET', actual: 'POST' })).toBe(
      'method',
    );
  });
});

describe('composing a request', () => {
  it('sends the method and URL as a request would carry them', () => {
    const result = toDescribedRequest({ ...emptyDraft(), method: 'post', url: '/api/orders' });

    expect(result).toEqual({ ok: true, request: { method: 'POST', url: '/api/orders' } });
  });

  it('leaves empty boxes out rather than sending them empty', () => {
    // An absent header block means "a request with no headers". An empty string
    // would be a header whose value is the empty string, which is a different
    // request and would be scored as one.
    const result = toDescribedRequest({ ...emptyDraft(), headers: '\n  \n', body: '' });

    expect(result.ok).toBe(true);
    expect(result.ok && result.request).toEqual({ method: 'GET', url: '/' });
  });

  it('carries headers, cookies and a body when they are filled in', () => {
    const result = toDescribedRequest({
      method: 'POST',
      url: '/api/orders?dryRun=true',
      headers: 'Content-Type: application/json\nAccept: application/json',
      cookies: 'session: abc123',
      body: '{"id":1}',
    });

    expect(result.ok && result.request).toEqual({
      method: 'POST',
      url: '/api/orders?dryRun=true',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      cookies: { session: 'abc123' },
      body: '{"id":1}',
    });
  });

  it('splits a header on its first colon, since a URL value carries more', () => {
    const parsed = parseNameValueLines('Referer: https://example.test/a', 'headers');

    expect(parsed.ok && parsed.values).toEqual({ Referer: 'https://example.test/a' });
  });

  it('names the line of a pair it cannot read rather than sending it', () => {
    const result = toDescribedRequest({ ...emptyDraft(), headers: 'Accept: */*\nContent-Type' });

    expect(result.ok).toBe(false);
    expect(!result.ok && result.message).toContain('Line 2');
  });

  it('insists on a URL that starts at the root', () => {
    // The endpoint takes the path and query a request would carry. Anything
    // else is scored as written and answers three distances nobody can act on.
    const result = toDescribedRequest({ ...emptyDraft(), url: 'api/orders' });

    expect(result.ok).toBe(false);
    expect(!result.ok && result.message).toContain('/api/orders');
  });

  it('refuses an empty URL', () => {
    expect(toDescribedRequest({ ...emptyDraft(), url: '   ' }).ok).toBe(false);
  });
});

describe('carrying a recorded request into the form', () => {
  it('fills every box from the entry', () => {
    const draft = draftFromLoggedRequest(
      loggedRequest({
        method: 'POST',
        url: '/api/orders',
        headers: { Accept: 'application/json' },
        cookies: { session: 'abc123' },
        body: '{"id":1}',
      }),
    );

    expect(draft).toEqual({
      method: 'POST',
      url: '/api/orders',
      headers: 'Accept: application/json',
      cookies: 'session: abc123',
      body: '{"id":1}',
    });
  });

  it('takes the first value of a header that arrived more than once', () => {
    // A described request carries one literal value per name, so something has
    // to give. The form is text the reader can correct, which is a better
    // outcome than refusing to carry the entry across at all.
    const draft = draftFromLoggedRequest(
      loggedRequest({ headers: { Accept: ['application/json', 'text/plain'] } }),
    );

    expect(draft.headers).toBe('Accept: application/json');
  });
});
