// SPDX-License-Identifier: Apache-2.0

import type { DescribedRequest, NearMissList, RequestPattern } from '../types.js';
import { sinceParam, type JournalWindowOptions } from './shared.js';
import type { RequestOptions, Transport } from './transport.js';

/**
 * On-demand near-miss scoring, which answers "why did nothing match?".
 *
 * The two calls are mirror images and only one of them touches the journal.
 * {@link forRequest} ranks the current **stubs** against a request somebody
 * describes, and needs no journal at all — which matters, because the
 * deployment anyone debugging a stub that will not match is standing in front
 * of is the default one, with journaling off. {@link forRequestPattern} ranks
 * the **recorded requests** against a pattern, so it reads the journal and
 * reports code 1010 without one.
 *
 * The ranking is mockulus' own and does not reproduce WireMock's. Near-miss
 * output is a debugging aid outside the strict-compatibility surface, and no
 * matching decision depends on the order.
 */
export class NearMissesApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Ranks the current stub mappings against a described request, closest
   * first, and echoes the server's reading of the description on each near
   * miss — so a caller can see what was understood before reading the
   * distances.
   *
   * The argument is a *request*, not a request pattern: the values are literal
   * and the query string is read out of `url` rather than declared separately.
   * Writing `urlPath` here would be a criterion where a value belongs, which is
   * why the type refuses it.
   */
  async forRequest(request: DescribedRequest, options?: RequestOptions): Promise<NearMissList> {
    return this.transport.send<NearMissList>({
      method: 'POST',
      path: '/__admin/near-misses/request',
      body: request,
      ...options,
    });
  }

  /**
   * Ranks the recorded requests against a supplied pattern, closest first —
   * how a team asks "what did arrive, and how close was it to what I expected?"
   * after a verification came back empty.
   *
   * Ties break on the entry id, which is time-ordered, so the same journal and
   * the same pattern produce the same answer every time.
   */
  async forRequestPattern(
    pattern: RequestPattern,
    options?: JournalWindowOptions,
  ): Promise<NearMissList> {
    const { since, ...rest }: JournalWindowOptions = options ?? {};
    return this.transport.send<NearMissList>({
      method: 'POST',
      path: '/__admin/near-misses/request-pattern',
      body: pattern,
      query: { since: sinceParam(since) },
      ...rest,
    });
  }
}
