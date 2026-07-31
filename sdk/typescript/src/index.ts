// SPDX-License-Identifier: Apache-2.0

/**
 * `@mockulus/admin-sdk` — a typed client for the mockulus admin API.
 *
 * The package's compatibility claim is the server's own: it types the
 * **supported subset** and nothing more, so a stub this SDK can express is a
 * stub the server registers. That moves mockulus' fail-loud contract from a 422
 * at registration to a type error before the call is written.
 *
 * What is here today is the client and the documents it exchanges. The
 * WireMock-style builders and the test helpers land in the releases that
 * follow; nothing below is waiting on them.
 */

export { MockulusClient } from './client/client.js';

export { ErrorCode, ErrorCodeStatus } from './codes.js';
export type { ErrorCodeValue } from './codes.js';

export { MockulusError, isMockulusError } from './errors.js';
export type { MockulusProblem } from './errors.js';

export type { MockulusClientOptions, RequestOptions } from './client/transport.js';
export type { JournalWindowOptions } from './client/shared.js';

// The namespace classes are exported for their types rather than to be
// constructed: a caller writing a helper that takes "whatever `client.mappings`
// is" needs a name for it, and `InstanceType<typeof MockulusClient>['mappings']`
// is not one.
export { MappingsApi } from './client/mappings.js';
export type { ListMappingsOptions, PaginateMappingsOptions } from './client/mappings.js';
export { RequestsApi } from './client/requests.js';
export type { ListServeEventsOptions } from './client/requests.js';
export { NearMissesApi } from './client/near-misses.js';
export { ScenariosApi } from './client/scenarios.js';
export { FilesApi } from './client/files.js';
export type { FileBody, PutFileOptions } from './client/files.js';
export { SettingsApi } from './client/settings.js';
export { SystemApi } from './client/system.js';

export type {
  ContentMatcher,
  DescribedRequest,
  Difference,
  Health,
  ImportOptions,
  ListMeta,
  LoggedRequest,
  LoggedRequestList,
  MatchResult,
  NearMiss,
  NearMissList,
  RemovedServeEvents,
  RequestCount,
  RequestPattern,
  ResponseDefinition,
  Scenario,
  ScenarioList,
  ServeEvent,
  ServeEventList,
  Settings,
  SettingsEnvelope,
  StubMapping,
  StubMappingImport,
  StubMappingList,
  VersionInfo,
} from './types.js';
