// SPDX-License-Identifier: Apache-2.0
import type { LoggedRequest, ServeEvent } from '@mockulus/admin-sdk';

/**
 * Reading a page of the request journal, with no Svelte and no network in it.
 *
 * The journal view is mostly windowing and presentation, and both are worth
 * testing against the awkward documents the server can legitimately answer with
 * — an entry with no `loggedDate`, a header that arrived three times, a body cut
 * at the cap — without mounting a component to do it.
 */

/** Which outcome the list is narrowed to. The three tabs of the journal view. */
export type JournalTab = 'all' | 'matched' | 'unmatched';

export interface JournalTabDefinition {
  readonly id: JournalTab;
  readonly label: string;
}

/**
 * The tabs, in the order they are shown. `all` leads because it is the reading
 * somebody arriving at the page wants: what has this replica served at all.
 */
export const JOURNAL_TABS: readonly JournalTabDefinition[] = [
  { id: 'all', label: 'All' },
  { id: 'matched', label: 'Matched' },
  { id: 'unmatched', label: 'Unmatched' },
];

/**
 * How many entries a read asks the server for.
 *
 * `limit` is the only pagination the journal has — there is no `offset` anywhere
 * under `/__admin/requests` — so this is a window on the newest entries rather
 * than a page number, and the view says so instead of drawing a pager it could
 * not honour.
 */
export const LIMIT_CHOICES: readonly number[] = [25, 50, 100, 500];

/** The default read: enough to see a test's traffic, small enough to render. */
export const DEFAULT_LIMIT = 50;

export interface SinceChoice {
  /** Minutes back from now, or 0 for no bound at all. */
  readonly minutes: number;
  readonly label: string;
}

export const SINCE_CHOICES: readonly SinceChoice[] = [
  { minutes: 0, label: 'Everything the journal holds' },
  { minutes: 5, label: 'The last 5 minutes' },
  { minutes: 15, label: 'The last 15 minutes' },
  { minutes: 60, label: 'The last hour' },
  { minutes: 24 * 60, label: 'The last 24 hours' },
];

/**
 * How often auto-refresh re-reads.
 *
 * Five seconds, chosen against what the journal actually is: eventually
 * consistent within `journal_flush_interval` plus index lag, typically under
 * 500 ms, so anything faster mostly re-reads the same page. It is also a
 * deliberate lower bound on how hard an unattended tab can hit a deployment.
 */
export const AUTO_REFRESH_INTERVAL_MS = 5_000;

/** The `since` bound a window choice means at this moment, or none for "everything". */
export function sinceFrom(minutes: number, now: Date): Date | undefined {
  if (!Number.isFinite(minutes) || minutes <= 0) {
    return undefined;
  }
  return new Date(now.getTime() - minutes * 60_000);
}

/** The entries a tab shows. */
export function filterByTab(events: readonly ServeEvent[], tab: JournalTab): ServeEvent[] {
  if (tab === 'matched') {
    return events.filter((event) => event.wasMatched);
  }
  if (tab === 'unmatched') {
    return events.filter((event) => !event.wasMatched);
  }
  return [...events];
}

export interface OutcomeCounts {
  readonly all: number;
  readonly matched: number;
  readonly unmatched: number;
}

/**
 * How many entries each tab holds.
 *
 * Counted over the window that was read rather than asked of the server,
 * deliberately. `GET /__admin/requests/unmatched` would answer the unmatched
 * question over the whole scan window and takes no `limit`, so its count and
 * this page's `limit` entries would be two different populations sitting next to
 * each other with no way for a reader to tell. One read, counted three ways,
 * means every number on the page describes the same set of requests.
 */
export function countsByOutcome(events: readonly ServeEvent[]): OutcomeCounts {
  let matched = 0;
  for (const event of events) {
    if (event.wasMatched) {
      matched += 1;
    }
  }
  return { all: events.length, matched, unmatched: events.length - matched };
}

/** When the entry was recorded, or `undefined` for a request that was never journalled. */
export function loggedAt(request: LoggedRequest): Date | undefined {
  // The RFC 3339 spelling first: it is what the server rendered, so it needs no
  // arithmetic here. `loggedDate` is the same instant in milliseconds and is the
  // fallback for a document that carried only the numeric form.
  if (request.loggedDateString !== undefined && request.loggedDateString !== '') {
    const parsed = new Date(request.loggedDateString);
    if (!Number.isNaN(parsed.getTime())) {
      return parsed;
    }
  }
  if (typeof request.loggedDate === 'number' && Number.isFinite(request.loggedDate)) {
    return new Date(request.loggedDate);
  }
  return undefined;
}

/**
 * The clock time an entry carries, for the row.
 *
 * Time of day rather than a full timestamp: every row on the page is from the
 * same session, so the date is the same on all of them and repeating it would
 * cost the width the URL needs. The detail panel prints the whole instant.
 */
export function formatClockTime(date: Date): string {
  return date.toLocaleTimeString();
}

/** How a request is named in one line — a row's accessible name, and its heading. */
export function describeRequest(request: LoggedRequest): string {
  return `${request.method} ${request.url}`;
}

/** One name-and-value pair out of the multi-valued maps a logged request carries. */
export interface NameValue {
  readonly name: string;
  readonly value: string;
}

/**
 * Flattens one of the repeated-value maps into rows.
 *
 * A header that arrived three times is an array and one that arrived once is a
 * bare string, so both spellings are collapsed to the text a reader sees. Sorted
 * by name because the wire order is arrival order, which is neither meaningful
 * nor stable between two requests a reader is comparing.
 */
function rowsOf(map: Readonly<Record<string, string | string[]>>): NameValue[] {
  return Object.entries(map)
    .map(([name, value]) => ({ name, value: Array.isArray(value) ? value.join(', ') : value }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

export function headerRows(request: LoggedRequest): NameValue[] {
  return rowsOf(request.headers);
}

export function cookieRows(request: LoggedRequest): NameValue[] {
  return rowsOf(request.cookies);
}

/**
 * The query parameters, out of WireMock's own shape — one object per name
 * carrying that name and every value it had.
 */
export function queryRows(request: LoggedRequest): NameValue[] {
  return Object.values(request.queryParams)
    .map((parameter) => ({ name: parameter.key, value: parameter.values.join(', ') }))
    .sort((a, b) => a.name.localeCompare(b.name));
}
