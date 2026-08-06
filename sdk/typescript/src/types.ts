// SPDX-License-Identifier: Apache-2.0

/**
 * The admin API's documents, under the names a caller writes.
 *
 * Every alias here points into `src/generated/types.ts`, which is produced from
 * `api/openapi.yaml` and gated against drift. Nothing is restated: a shape
 * written out by hand would be a second copy of the contract, and the second
 * copy is the one that goes stale without anyone noticing. What this file adds
 * is names — `StubMapping` rather than `components['schemas']['StubMapping']` —
 * and nothing else, for the reason set out at the end.
 */

import type { components } from './generated/types.js';

/** A stub mapping as the server stores and answers with one. */
export type StubMapping = components['schemas']['StubMapping'];
/** The list envelope `GET /__admin/mappings` and the metadata searches answer with. */
export type StubMappingList = components['schemas']['StubMappingList'];
/** The `{ total }` a listing carries alongside its page. */
export type ListMeta = components['schemas']['ListMeta'];
/** A batch import and the options that govern how it is applied. */
export type StubMappingImport = components['schemas']['StubMappingImport'];
/** How an import resolves an id that is already taken, and whether it prunes. */
export type ImportOptions = components['schemas']['ImportOptions'];
/** The criteria half of a stub, and the body of every verification query. */
export type RequestPattern = components['schemas']['RequestPattern'];
/** What a matching request is served. */
export type ResponseDefinition = components['schemas']['ResponseDefinition'];
/** One criterion over a value — the vocabulary shared by every matcher position. */
export type ContentMatcher = components['schemas']['ContentMatcher'];

/** A recorded request, or the server's reading of a described one. */
export type LoggedRequest = components['schemas']['LoggedRequest'];
/** A whole journal entry: the request, what was served, and which stub matched. */
export type ServeEvent = components['schemas']['ServeEvent'];
/** The envelope `GET /__admin/requests` answers with. */
export type ServeEventList = components['schemas']['ServeEventList'];
/** The envelope `find` and `unmatched` answer with — logged requests, no `meta`. */
export type LoggedRequestList = components['schemas']['LoggedRequestList'];
/** What `remove` deleted, as whole serve events. */
export type RemovedServeEvents = components['schemas']['RemovedServeEvents'];
/** The answer to a verification. */
export type RequestCount = components['schemas']['RequestCount'];

/** One request-and-candidate pairing, with the distance between them. */
export type NearMiss = components['schemas']['NearMiss'];
/** The envelope every near-miss endpoint answers with. */
export type NearMissList = components['schemas']['NearMissList'];
/** How far one candidate was from matching, and which criteria did not line up. */
export type MatchResult = components['schemas']['MatchResult'];
/** One criterion that did not line up. */
export type Difference = components['schemas']['Difference'];
/** A literal request to score the current stubs against. */
export type DescribedRequest = components['schemas']['DescribedRequest'];

/** One scenario, its current state, and the states its stubs name. */
export type Scenario = components['schemas']['Scenario'];
/** The envelope `GET /__admin/scenarios` answers with. */
export type ScenarioList = components['schemas']['ScenarioList'];

/** The deployment-wide settings document. */
export type Settings = components['schemas']['Settings'];
/** WireMock's wrapper around the settings document. */
export type SettingsEnvelope = components['schemas']['SettingsEnvelope'];
/** The health document: WireMock's shape plus this replica's store and epoch. */
export type Health = components['schemas']['Health'];
/** The version document, including the WireMock surface claim. */
export type VersionInfo = components['schemas']['VersionInfo'];

/**
 * There is deliberately no second, looser type for the write side.
 *
 * A schema on this surface is one type in both directions because the server
 * reuses one: `RequestPattern` is the criteria half of a stub and also the body
 * of a verification, and a `StubMapping` is both what a caller sends and what
 * comes back. The one thing that would have forced a split — a member carrying
 * a `default` in the contract arriving *required* in the generated type, so
 * that every create had to spell out `priority: 5, persistent: false` — is
 * settled where it belongs, in the generator invocation, which passes
 * `--default-non-nullable false` for exactly this reason.
 *
 * So {@link StubMapping} is what `mappings.create` takes as well as what it
 * answers, and the type a caller writes is the type they read back.
 */
