// SPDX-License-Identifier: Apache-2.0
import type { StubMapping } from '@mockulus/admin-sdk';

/**
 * Reading and narrowing a set of stub mappings, with no Svelte and no network
 * in it. The list view is mostly this, and this is the half that can be tested
 * against awkward documents — a stub with no `request` at all, a URL criterion
 * written five different ways — without mounting anything.
 */

/** How many rows the list renders at once. See {@link pageOf} for why there is a cap. */
export const PAGE_SIZE = 50;

/**
 * The wall the whole-snapshot read stops at.
 *
 * The list filters in the browser (below), which means the snapshot has to be
 * in the browser, which means an unbounded deployment would be an unbounded
 * read. Ten thousand is far past any deployment this is likely to meet and
 * still a bounded promise, and the view says out loud when it has been hit
 * rather than quietly showing a prefix of the truth.
 */
export const MAX_LOADED_MAPPINGS = 10_000;

/** The URL criterion a stub matches on, named by the field that carries it. */
export interface UrlCriterion {
  readonly kind: 'url' | 'urlPath' | 'urlPattern' | 'urlPathPattern' | 'urlPathTemplate';
  readonly value: string;
}

/**
 * The five spellings, in the order the list prefers to show them: the two exact
 * forms first, because they are what a reader recognises, then the patterns.
 * A stub may carry only one — the server refuses more — so the order decides
 * nothing but which name is read first.
 */
const URL_FIELDS = ['urlPath', 'url', 'urlPathTemplate', 'urlPattern', 'urlPathPattern'] as const;

/**
 * The stub's identity, under either of the two spellings the server echoes.
 *
 * `undefined` is unreachable for a document the server answered with — it
 * stamps one when the client supplies neither — but the contract types both
 * fields as optional, and a list that assumed otherwise would build a link to
 * `/stubs/undefined` on the one document that proved it wrong.
 */
export function stubIdOf(mapping: StubMapping): string | undefined {
  return mapping.id ?? mapping.uuid;
}

/** The URL criterion a stub matches on, or `undefined` when it matches any URL. */
export function urlCriterionOf(mapping: StubMapping): UrlCriterion | undefined {
  const request = mapping.request;
  if (!request) {
    return undefined;
  }
  for (const kind of URL_FIELDS) {
    const value = request[kind];
    if (typeof value === 'string' && value !== '') {
      return { kind, value };
    }
  }
  return undefined;
}

/**
 * The method a stub matches on.
 *
 * `ANY` when the stub does not say, which is the server's own reading of an
 * absent `method` and therefore what the list should show rather than a blank
 * that could be mistaken for missing data.
 */
export function methodOf(mapping: StubMapping): string {
  const method = mapping.request?.method?.trim();
  return method === undefined || method === '' ? 'ANY' : method.toUpperCase();
}

/** What the list is narrowed by in the browser. Metadata is narrowed by the server. */
export interface StubFilters {
  /** An exact method, or the empty string for any. */
  readonly method: string;
  /** A case-insensitive substring of the URL criterion. */
  readonly url: string;
}

/**
 * Narrows a set of stubs.
 *
 * Method and URL are filtered here rather than by the server because the admin
 * API has no parameter for either — `GET /__admin/mappings` takes `limit` and
 * `offset` and nothing else. A filter that searched only the page the server
 * happened to return would answer a different question from the one the input
 * box asks, and would answer it differently depending on how far the user had
 * paged, so the snapshot is read whole and narrowed here.
 *
 * A stub with no URL criterion matches every request, so it survives an empty
 * URL filter and no other: there is no text in it for a substring to be found
 * in, and reporting it under every search would make the filter useless exactly
 * on the deployments that have such stubs.
 */
export function filterMappings(
  mappings: readonly StubMapping[],
  filters: StubFilters,
): StubMapping[] {
  const method = filters.method.trim().toUpperCase();
  const url = filters.url.trim().toLowerCase();

  return mappings.filter((mapping) => {
    if (method !== '' && methodOf(mapping) !== method) {
      return false;
    }
    if (url === '') {
      return true;
    }
    const criterion = urlCriterionOf(mapping);
    return criterion !== undefined && criterion.value.toLowerCase().includes(url);
  });
}

/**
 * The methods present in a set, sorted, so the filter offers what is there
 * rather than a fixed list of verbs — a deployment standing in for a service
 * with a method WireMock's own enumeration does not define still gets one.
 */
export function methodsPresent(mappings: readonly StubMapping[]): string[] {
  return [...new Set(mappings.map(methodOf))].sort((a, b) => a.localeCompare(b));
}

/** How many pages a set of this size fills. Always at least one, so "1 of 1" holds when empty. */
export function pageCount(total: number, size: number = PAGE_SIZE): number {
  return Math.max(1, Math.ceil(total / size));
}

/**
 * The slice of a set that a page shows.
 *
 * This is what keeps the DOM bounded. A deployment may hold thousands of stubs
 * and the browser renders at most {@link PAGE_SIZE} rows of them, whatever the
 * filters leave behind. `page` is clamped rather than validated because it is
 * driven by buttons and by filter changes that can shrink the set underneath
 * it, and a page number that has gone out of range is a stale number rather
 * than a mistake worth reporting.
 */
export function pageOf<T>(items: readonly T[], page: number, size: number = PAGE_SIZE): T[] {
  const last = pageCount(items.length, size) - 1;
  const clamped = Math.min(Math.max(0, Math.trunc(page)), last);
  return items.slice(clamped * size, clamped * size + size);
}

/** The 1-based range a page covers, for the "showing … of …" line. Empty sets read 0–0. */
export function pageRange(
  total: number,
  page: number,
  size: number = PAGE_SIZE,
): { first: number; last: number } {
  if (total === 0) {
    return { first: 0, last: 0 };
  }
  const clamped = Math.min(Math.max(0, Math.trunc(page)), pageCount(total, size) - 1);
  return { first: clamped * size + 1, last: Math.min(total, (clamped + 1) * size) };
}
