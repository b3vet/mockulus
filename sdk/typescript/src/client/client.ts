// SPDX-License-Identifier: Apache-2.0

import { FilesApi } from './files.js';
import { MappingsApi } from './mappings.js';
import { NearMissesApi } from './near-misses.js';
import { RequestsApi } from './requests.js';
import { ScenariosApi } from './scenarios.js';
import { SettingsApi } from './settings.js';
import { SystemApi } from './system.js';
import { Transport, type MockulusClientOptions } from './transport.js';

/**
 * A typed client for one mockulus deployment's admin API.
 *
 * The calls are grouped the way the contract is, one namespace per tag, so a
 * reader who knows the API knows where to look and a reader who does not can
 * find an operation without a search. Every namespace shares one
 * {@link Transport}: URL building, the token header, JSON encoding, error
 * mapping and the timeout are decided in one place rather than seven.
 *
 * Three properties of this surface surprise people arriving from a WireMock
 * client, and every namespace that carries one says so on the method:
 *
 * - The **request journal is off by default**, so every call under
 *   {@link requests} answers 500 with code 1010 until `journal_enabled` is set.
 *   That is a configuration answer rather than a failure, which is why
 *   `MockulusError.isJournalDisabled` exists to be branched on.
 * - The journal is **eventually consistent**, so a verification issued
 *   immediately after the traffic it verifies should poll rather than assert
 *   once.
 * - Several answers are **bodyless**: an unknown stub or serve-event id is a
 *   bare 404, an import or a settings write is an empty 200, a file upload is
 *   an empty 201. The methods that see one resolve to nothing or, where an id
 *   is involved, offer a `…OrNull` variant beside the throwing default.
 *
 * What this client does *not* do is invent shapes. It answers the envelopes the
 * contract defines rather than unwrapping them into arrays, because the SDK's
 * claim is that its types are the contract's types — a shape invented here is a
 * shape nothing checks. {@link MappingsApi.paginate} is the one convenience
 * over that rule, and it is a way of making the requests rather than a way of
 * reshaping the answers.
 *
 * ```ts
 * const client = new MockulusClient({ baseUrl: 'http://localhost:9090' });
 * await client.mappings.create({
 *   request: { method: 'GET', urlPath: '/api/orders' },
 *   response: { status: 200, jsonBody: { orders: [] } },
 * });
 * ```
 */
export class MockulusClient {
  /** Stub mappings: register, read, replace, delete, import and search. */
  readonly mappings: MappingsApi;
  /** The request journal and the verification queries over it. */
  readonly requests: RequestsApi;
  /** On-demand near-miss scoring, which answers "why did nothing match?". */
  readonly nearMisses: NearMissesApi;
  /** Stateful mocks: what the current stubs define, and where they are. */
  readonly scenarios: ScenariosApi;
  /** The response-body file store, which backs `bodyFileName`. */
  readonly files: FilesApi;
  /** The deployment's global settings document. */
  readonly settings: SettingsApi;
  /** Health, version, the combined reset and the drain. */
  readonly system: SystemApi;

  constructor(options: MockulusClientOptions) {
    // One transport, shared. The namespaces hold it rather than the client, so
    // each depends on a small surface instead of on the client and therefore on
    // each other.
    const transport = new Transport(options);
    this.mappings = new MappingsApi(transport);
    this.requests = new RequestsApi(transport);
    this.nearMisses = new NearMissesApi(transport);
    this.scenarios = new ScenariosApi(transport);
    this.files = new FilesApi(transport);
    this.settings = new SettingsApi(transport);
    this.system = new SystemApi(transport);
  }
}
