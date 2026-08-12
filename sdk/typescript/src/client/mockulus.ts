// SPDX-License-Identifier: Apache-2.0

import type { StubMappingImport, ValidationReport } from '../types.js';
import type { RequestOptions, Transport } from './transport.js';

/**
 * The endpoints that are mockulus' own rather than WireMock's.
 *
 * They live under the reserved `/__admin/mockulus/**` prefix (SPEC §5.7), and
 * this namespace mirrors that so a caller can tell at a glance which side of the
 * line a call is on. Nothing here exists on a WireMock server, and nothing here
 * is required: a suite that never touches this namespace still has the whole
 * compatibility surface.
 */
export class MockulusApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Reports what importing this batch would refuse, and imports none of it.
   *
   * The question this answers — how much of a mappings set is inside the
   * supported subset — otherwise costs a deployment to ask, because the only
   * other way to get it is to register the stubs somewhere and read the
   * refusals. Nothing is written here: no document, no snapshot rebuild, no
   * epoch change, no journal entry.
   *
   * **A refused mapping is the answer, not an error.** A batch in which every
   * mapping is invalid still resolves rather than throwing, so read
   * {@link ValidationReport.wouldImport} rather than catching. The call rejects
   * only for the reasons any call does: an unreadable envelope, a failed token
   * check, a body over the limit.
   *
   * ```ts
   * const report = await client.mockulus.validate({ mappings });
   * if (!report.wouldImport) {
   *   for (const result of report.results) {
   *     if (!result.valid) console.error(result.index, result.errors);
   *   }
   * }
   * ```
   *
   * Read `wouldImport` rather than `valid` when what you are about to do is an
   * import. They differ in intent rather than in value — import is atomic, so
   * one bad mapping in fifty writes nothing at all, and `wouldImport` is the
   * field that says so.
   *
   * Each entry in `errors` is exactly what a real registration would have
   * answered for that mapping: same code, same pointer, same detail. The server
   * runs the registrar's own validation rather than a second implementation of
   * it.
   */
  async validate(batch: StubMappingImport, options?: RequestOptions): Promise<ValidationReport> {
    return this.transport.send<ValidationReport>({
      method: 'POST',
      path: '/__admin/mockulus/validate',
      body: batch,
      ...options,
    });
  }
}
