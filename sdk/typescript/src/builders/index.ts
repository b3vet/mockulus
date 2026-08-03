// SPDX-License-Identifier: Apache-2.0

/**
 * The WireMock-familiar layer: builders that produce the mapping documents the
 * client registers.
 *
 * The package's compatibility claim is the server's own — it types the
 * supported subset and nothing more — and this is where that claim is at its
 * strongest. Nothing outside the subset exists to call: there is no
 * `equalToXml`, no `matchesXPath`, no `proxyBaseUrl`, no `postServeActions`, so
 * the 422 those fields would earn is not a runtime failure a suite discovers,
 * it is a name that does not resolve.
 *
 * The refusals that depend on how fields are *combined* are carried the same
 * way wherever a type can carry them: a modifier is a parameter of the matcher
 * it modifies, a verb takes exactly one URL criterion, a path-parameter
 * criterion names a variable its template binds, a scenario state follows a
 * scenario, and a response has exactly one body form. What is left is the
 * refusals that depend on a *value* — a regular expression that does not
 * compile, a date-time spelling that is not one, a schema that is not a schema.
 * Those reach the server, which answers 422 with a pointer at the offending
 * field; no builder can know them without shipping the engines that decide them.
 *
 * The names are WireMock's Java DSL's, because the point of this layer is that
 * a team's existing muscle memory works. Most of them are the JSON field's name
 * too; these are the ones that are not, and this list is here so that a reader
 * holding `api/openapi.yaml` open can get from a field to the call that writes
 * it:
 *
 * | Builder | Field |
 * |---|---|
 * | `equalToIgnoreCase` | `equalTo` + `caseInsensitive` |
 * | `containing` / `notContaining` | `contains` / `doesNotContain` |
 * | `matching` / `notMatching` | `matches` / `doesNotMatch` |
 * | `matchingJsonPath` / `notMatchingJsonPath` | `matchesJsonPath` / `doesNotMatchJsonPath` |
 * | `matchingJsonSchema` | `matchesJsonSchema` |
 * | `urlEqualTo` / `urlMatching` | `url` / `urlPattern` |
 * | `urlPathEqualTo` / `urlPathMatching` | `urlPath` / `urlPathPattern` |
 * | `withRequestBody` | one entry of `bodyPatterns` |
 * | `withBodyFile` | `bodyFileName` |
 * | `withFixedDelay` | `fixedDelayMilliseconds` |
 * | `withUniformRandomDelay` / `withLogNormalRandomDelay` | `delayDistribution` |
 * | `persistent` | `persistent` |
 * | `del` | `method: DELETE` — `delete` is an operator and cannot be a binding |
 */

export {
  absent,
  after,
  and,
  before,
  binaryEqualTo,
  containing,
  equalTo,
  equalToDateTime,
  equalToIgnoreCase,
  equalToJson,
  JsonUnit,
  jsonUnitRegex,
  matching,
  matchingJsonPath,
  matchingJsonSchema,
  not,
  notContaining,
  notMatching,
  notMatchingJsonPath,
  nowOffset,
  or,
} from './matchers.js';

export type {
  BodyOnlyMatcher,
  BodyPattern,
  DateTimeTruncation,
  EqualToJsonOptions,
  JsonSchemaOptions,
  JsonSchemaVersion,
  LiteralDateTimeOptions,
  Matcher,
  NowRelative,
  RelativeDateTimeOptions,
  TemporalUnit,
} from './matchers.js';

export {
  any,
  anyUrl,
  del,
  get,
  head,
  MappingBuilder,
  options,
  patch,
  post,
  put,
  request,
  stubFor,
  trace,
  urlEqualTo,
  urlMatching,
  urlPathEqualTo,
  urlPathMatching,
  urlPathTemplate,
  UrlCriterion,
} from './request.js';

export type { AnyMappingBuilder, PathVariables } from './request.js';

export { aResponse, ResponseBuilder } from './response.js';

export type { Fault, ResponseTransformer, TransformerParameters } from './response.js';
