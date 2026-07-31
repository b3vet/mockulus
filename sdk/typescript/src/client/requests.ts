// SPDX-License-Identifier: Apache-2.0

import type {
  LoggedRequestList,
  NearMissList,
  RemovedServeEvents,
  RequestCount,
  RequestPattern,
  ServeEvent,
  ServeEventList,
} from '../types.js';
import {
  encodeSegment,
  nullOnBodylessNotFound,
  sinceParam,
  type JournalWindowOptions,
} from './shared.js';
import type { RequestOptions, Transport } from './transport.js';

/** Which window and how much of it the journal listing wants. */
export interface ListServeEventsOptions extends JournalWindowOptions {
  /**
   * How many serve events to return, newest first. This is the **only**
   * pagination parameter the journal has and it exists on this one endpoint:
   * there is no `offset` anywhere under `/__admin/requests`, and no `limit` on
   * the unmatched or criteria queries. It trims the page after the window is
   * counted, which is why `meta.total` can exceed the array's length.
   */
  limit?: number;
}

/**
 * The request journal and the verification queries over it.
 *
 * Every call here needs `journal_enabled: true`. Without it the server answers
 * 500 with code 1010 — a configuration answer rather than a failure, and the
 * reason to branch on `MockulusError.isJournalDisabled` rather than on an empty
 * result, which is not what this answers with. The journal is off by
 * default, because mockulus makes every expensive feature pay-per-use.
 *
 * It is also **eventually consistent**: an entry becomes visible within
 * `journal_flush_interval` plus index lag, typically under 500 ms. A
 * verification issued immediately after the traffic it verifies should poll for
 * the window it expects rather than assert once.
 */
export class RequestsApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Reads the journal newest first, as whole serve events.
   *
   * Half the endpoints in this group answer with serve events and half with the
   * bare logged requests inside them. This is one of the former;
   * {@link find} and {@link unmatched} are the latter.
   */
  async list(options?: ListServeEventsOptions): Promise<ServeEventList> {
    const { limit, since, ...rest }: ListServeEventsOptions = options ?? {};
    return this.transport.send<ServeEventList>({
      method: 'GET',
      path: '/__admin/requests',
      query: { limit, since: sinceParam(since) },
      ...rest,
    });
  }

  /**
   * Reads one entry by the id a listing carried.
   *
   * Throws on an id that names nothing, which the server answers with a bare
   * 404 and no body — the same not-found an unknown stub id gets, and
   * {@link getOrNull} is the variant for a caller to whom that is an ordinary
   * answer.
   */
  async get(id: string, options?: RequestOptions): Promise<ServeEvent> {
    return this.transport.send<ServeEvent>({
      method: 'GET',
      path: `/__admin/requests/${encodeSegment(id)}`,
      ...options,
    });
  }

  /** {@link get}, answering `null` where it would throw the bodyless 404. */
  async getOrNull(id: string, options?: RequestOptions): Promise<ServeEvent | null> {
    return nullOnBodylessNotFound(() => this.get(id, options));
  }

  /**
   * Deletes one entry.
   *
   * Idempotent, and deliberately silent about a missing entry: an id that names
   * nothing answers 200 like any other, because the caller's goal — that entry
   * not being in the journal — holds either way. There is no `deleteOrNull`
   * here for that reason, and inventing one would imply a distinction the
   * server does not draw.
   */
  async delete(id: string, options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'DELETE',
      path: `/__admin/requests/${encodeSegment(id)}`,
      accept: 'none',
      ...options,
    });
  }

  /** Empties the journal. Deployment-wide, like every other reset on this surface. */
  async clear(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'DELETE',
      path: '/__admin/requests',
      accept: 'none',
      ...options,
    });
  }

  /**
   * Counts the recorded requests that satisfy a pattern.
   *
   * The pattern is the same model a stub's `request` is, compiled through the
   * same compiler. That is what makes a verification and the stub it was copied
   * from describe the same requests; two models would let a test pass against a
   * stub that would never have matched.
   */
  async count(pattern: RequestPattern, options?: JournalWindowOptions): Promise<RequestCount> {
    return this.transport.send<RequestCount>({
      method: 'POST',
      path: '/__admin/requests/count',
      body: pattern,
      ...this.window(options),
    });
  }

  /**
   * Finds the recorded requests that satisfy a pattern.
   *
   * Answers the logged **requests**, not the serve events holding them, and the
   * envelope carries no `meta` — the query runs over the whole scan window
   * rather than a page of it, so there is no total to report that the array's
   * length does not already say.
   */
  async find(pattern: RequestPattern, options?: JournalWindowOptions): Promise<LoggedRequestList> {
    return this.transport.send<LoggedRequestList>({
      method: 'POST',
      path: '/__admin/requests/find',
      body: pattern,
      ...this.window(options),
    });
  }

  /**
   * Deletes the recorded requests that satisfy a pattern, and reports what went
   * under `serveEvents` — the one criteria query that answers with whole events
   * rather than the requests inside them, because a caller deleting entries is
   * entitled to see what was deleted.
   */
  async remove(
    pattern: RequestPattern,
    options?: JournalWindowOptions,
  ): Promise<RemovedServeEvents> {
    return this.transport.send<RemovedServeEvents>({
      method: 'POST',
      path: '/__admin/requests/remove',
      body: pattern,
      ...this.window(options),
    });
  }

  /**
   * Lists the recorded requests that matched no stub, which is the first thing
   * anyone looks at when a test fails.
   */
  async unmatched(options?: JournalWindowOptions): Promise<LoggedRequestList> {
    return this.transport.send<LoggedRequestList>({
      method: 'GET',
      path: '/__admin/requests/unmatched',
      ...this.window(options),
    });
  }

  /**
   * Scores every unmatched entry against the current stubs and answers which
   * ones came closest — the second thing anyone looks at, and the one that says
   * *why* nothing matched.
   *
   * The answer is one flat list of request-and-stub pairings rather than a list
   * grouped by request, which is the shape WireMock's clients deserialize.
   */
  async unmatchedNearMisses(options?: JournalWindowOptions): Promise<NearMissList> {
    return this.transport.send<NearMissList>({
      method: 'GET',
      path: '/__admin/requests/unmatched/near-misses',
      ...this.window(options),
    });
  }

  /**
   * Splits a journal window off the caller's options.
   *
   * `since` travels in the query string on every one of these endpoints,
   * including the three that carry their criteria in the body — which is where
   * a caller most wants it, because "since the test started" is the shape of
   * verification that survives a shared deployment.
   */
  private window(options: JournalWindowOptions | undefined): {
    query: { since: string | undefined };
  } & RequestOptions {
    const { since, ...rest }: JournalWindowOptions = options ?? {};
    return { query: { since: sinceParam(since) }, ...rest };
  }
}
