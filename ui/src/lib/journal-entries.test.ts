// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import {
  cookieRows,
  countsByOutcome,
  describeRequest,
  filterByTab,
  headerRows,
  loggedAt,
  queryRows,
  sinceFrom,
} from './journal-entries';
import { loggedRequest, serveEvent } from './journal-testing';

describe('journal windowing', () => {
  it('turns a minutes choice into a bound at that many minutes before the read', () => {
    const now = new Date('2026-07-29T09:15:00.000Z');

    expect(sinceFrom(15, now)?.toISOString()).toBe('2026-07-29T09:00:00.000Z');
  });

  it('has no bound for the choice that means everything the journal holds', () => {
    // Zero is the "everything" option rather than a bound of now, which would
    // window the read down to the entries recorded during the round trip.
    expect(sinceFrom(0, new Date())).toBeUndefined();
  });
});

describe('journal tabs', () => {
  const events = [
    serveEvent(1),
    serveEvent(2, { wasMatched: false }),
    serveEvent(3),
    serveEvent(4, { wasMatched: false }),
  ];

  it('shows everything on the all tab', () => {
    expect(filterByTab(events, 'all')).toHaveLength(4);
  });

  it('splits the window by outcome', () => {
    expect(filterByTab(events, 'matched').map((event) => event.id)).toEqual([
      events[0]?.id,
      events[2]?.id,
    ]);
    expect(filterByTab(events, 'unmatched').map((event) => event.id)).toEqual([
      events[1]?.id,
      events[3]?.id,
    ]);
  });

  it('counts all three tabs over the one window that was read', () => {
    // The counts and the rows have to describe the same population. Asking the
    // server for the unmatched count separately would answer over the whole
    // scan window, which takes no limit, and the two numbers on screen would be
    // about different sets of requests with nothing saying so.
    expect(countsByOutcome(events)).toEqual({ all: 4, matched: 2, unmatched: 2 });
  });

  it('counts an empty window as three zeros rather than as nothing', () => {
    expect(countsByOutcome([])).toEqual({ all: 0, matched: 0, unmatched: 0 });
  });
});

describe('reading a recorded request', () => {
  it('prefers the timestamp the server rendered over the millisecond count', () => {
    const at = loggedAt(
      loggedRequest({ loggedDateString: '2026-07-29T09:15:00.000Z', loggedDate: 0 }),
    );

    expect(at?.toISOString()).toBe('2026-07-29T09:15:00.000Z');
  });

  it('falls back to the millisecond count when that is all the entry carried', () => {
    const at = loggedAt(
      loggedRequest({
        loggedDateString: undefined,
        loggedDate: Date.parse('2026-07-29T09:15:00Z'),
      }),
    );

    expect(at?.toISOString()).toBe('2026-07-29T09:15:00.000Z');
  });

  it('reports no time rather than an invalid one when neither is readable', () => {
    // A described request echoed by the near-miss endpoints carries neither
    // field: it was never journalled, so there is no moment to report and an
    // Invalid Date rendered into a row would be a worse answer than none.
    expect(
      loggedAt(loggedRequest({ loggedDateString: 'not a timestamp', loggedDate: undefined })),
    ).toBeUndefined();
    expect(loggedAt(loggedRequest({ loggedDateString: undefined, loggedDate: undefined }))).toBe(
      undefined,
    );
  });

  it('names a request by its method and URL, which is what a row announces', () => {
    expect(describeRequest(loggedRequest({ method: 'POST', url: '/api/orders?dryRun=true' }))).toBe(
      'POST /api/orders?dryRun=true',
    );
  });

  it('collapses a header that arrived more than once into the text it carried', () => {
    const rows = headerRows(
      loggedRequest({ headers: { Accept: ['application/json', 'text/plain'], Host: 'mock.test' } }),
    );

    expect(rows).toEqual([
      { name: 'Accept', value: 'application/json, text/plain' },
      { name: 'Host', value: 'mock.test' },
    ]);
  });

  it('sorts by name, since arrival order is neither meaningful nor stable', () => {
    const rows = headerRows(loggedRequest({ headers: { zeta: '1', alpha: '2', Mu: '3' } }));

    expect(rows.map((row) => row.name)).toEqual(['alpha', 'Mu', 'zeta']);
  });

  it('reads the query parameters out of WireMock own shape', () => {
    const rows = queryRows(
      loggedRequest({
        queryParams: {
          tag: { key: 'tag', values: ['a', 'b'] },
          dryRun: { key: 'dryRun', values: ['true'] },
        },
      }),
    );

    expect(rows).toEqual([
      { name: 'dryRun', value: 'true' },
      { name: 'tag', value: 'a, b' },
    ]);
  });

  it('reads cookies the same way headers are read', () => {
    expect(cookieRows(loggedRequest({ cookies: { session: 'abc123' } }))).toEqual([
      { name: 'session', value: 'abc123' },
    ]);
  });
});
