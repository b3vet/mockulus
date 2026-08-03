// SPDX-License-Identifier: Apache-2.0

/**
 * The documents this layer refuses to build, asserted as type errors.
 *
 * This file is deliberately **not** a vitest case, and the file extension says
 * so: there is nothing to run. Every assertion here is a `@ts-expect-error`, and
 * the thing that checks them is `pnpm run check`, which type-checks `test/` as
 * well as `src/`. A directive that stops being needed — because a refusal was
 * relaxed, or because a signature drifted and the line now fails for a different
 * reason — is itself an error ("Unused '@ts-expect-error' directive"), so these
 * cannot rot into silence the way a comment saying the same thing would.
 *
 * Each one is a shape the server answers 422 to, and every one of them is a
 * shape WireMock accepts and then behaves oddly around. That asymmetry is the
 * argument for carrying them here: the 422 is already the good outcome, and a
 * name that does not resolve is a better one.
 *
 * The bodies are never executed. They are written as one exported function so
 * nothing here is an unused declaration, and so a reader can see the whole set
 * at once.
 */

import {
  absent,
  after,
  and,
  anyUrl,
  aResponse,
  before,
  binaryEqualTo,
  containing,
  equalTo,
  equalToDateTime,
  equalToJson,
  get,
  matching,
  matchingJsonPath,
  matchingJsonSchema,
  not,
  or,
  post,
  urlPathEqualTo,
  urlPathTemplate,
} from '../../src/index.js';

/** The refusals that come from a modifier having nothing to modify. */
export function misplacedModifiers(): void {
  // `schemaVersion` rides along with `matchesJsonSchema` and only with it.
  // WireMock drops it silently anywhere else — and drops it *before* validating
  // the value, so `{"equalTo":"x","schemaVersion":"BANANA"}` registers cleanly
  // there. There is no `schemaVersion()` here to attach to anything, so the
  // only way to write one is on the matcher that reads it.
  matchingJsonSchema({ type: 'object' }, { schemaVersion: 'V7' });
  // @ts-expect-error a draft outside the five spellings; the set is exact and case-sensitive
  matchingJsonSchema({ type: 'object' }, { schemaVersion: 'v7' });
  // @ts-expect-error there is no second parameter on equalTo to carry a draft
  equalTo('x', { schemaVersion: 'V7' });

  // The temporal modifiers ride along with `before`, `after` or
  // `equalToDateTime`. On a combinator the format never reaches the leaves that
  // would use it, and on an `equalTo` a date pattern means nothing at all;
  // WireMock accepts both and silently drops them.
  after('now', { actualFormat: 'unix' });
  // @ts-expect-error containing takes no modifiers, so an actualFormat has nothing to modify
  containing('2021', { actualFormat: 'unix' });
  // @ts-expect-error a combinator is not a date-time matcher
  and(before('now'), after('now'), { truncateActual: 'FIRST_HOUR_OF_DAY' });

  // `truncateExpected` is inert on a literal expected value: WireMock applies it
  // only to a now-relative operand, and the server refuses the pair rather than
  // accepting a parameter that could not take effect (deviation #50).
  before('now +3 days', { truncateExpected: 'FIRST_DAY_OF_MONTH' });
  // @ts-expect-error truncateExpected has no effect on a literal date-time
  before('2021-06-14T00:00:00Z', { truncateExpected: 'FIRST_DAY_OF_MONTH' });

  // `applyTruncationLast` chooses whether the truncation or the offset is
  // applied first, so without a truncation there is no order to choose.
  equalToDateTime('now +3 days', {
    truncateExpected: 'FIRST_DAY_OF_MONTH',
    applyTruncationLast: true,
  });
  // @ts-expect-error applyTruncationLast has no effect without truncateExpected
  equalToDateTime('now +3 days', { applyTruncationLast: true });

  // `truncateActual` truncates a value that parsed to a zoned instant, which a
  // custom pattern never yields — only ISO-8601, `unix` and `epoch` do.
  after('now', { actualFormat: 'epoch', truncateActual: 'FIRST_HOUR_OF_DAY' });
  // @ts-expect-error truncateActual is inert beside a pattern actualFormat
  after('now', { actualFormat: 'dd/MM/yyyy', truncateActual: 'FIRST_HOUR_OF_DAY' });

  // The three names this project's own spec and shipped code carried without
  // their existing anywhere in WireMock. The server now answers each with the
  // real parameter's name; here they are not parameters at all.
  // @ts-expect-error the parameter is truncateExpected
  before('now +3 days', { truncateExpectedTo: 'FIRST_DAY_OF_MONTH' });
  // @ts-expect-error the parameter is truncateActual
  before('now +3 days', { truncateActualTo: 'FIRST_DAY_OF_MONTH' });
  // @ts-expect-error there is no offset parameter — the offset is written into the expected value
  before('now', { expectedOffset: '3 days' });

  // The two flags `equalToJson` reads, and nothing else's.
  equalToJson({ a: 1 }, { ignoreArrayOrder: true, ignoreExtraElements: true });
  // @ts-expect-error ignoreArrayOrder has nothing to modify beside a string equality
  equalTo('x', { ignoreArrayOrder: true });
  // @ts-expect-error caseInsensitive is spelled as equalToIgnoreCase, and reaches nothing else
  containing('x', { caseInsensitive: true });
}

/** The refusals that come from where a matcher may appear. */
export function misplacedMatchers(): void {
  // `binaryEqualTo` compares the subject's raw bytes, so it is accepted only in
  // `bodyPatterns` and only at the top level. WireMock declares its combinators
  // and the object form of `matchesJsonPath` over string patterns, so a nested
  // one is refused there and here.
  post(anyUrl()).withRequestBody(binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error a header has no raw bytes to compare against
  get(anyUrl()).withHeader('X-Raw', binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error nor does a query parameter
  get(anyUrl()).withQueryParam('raw', binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error nor a cookie
  get(anyUrl()).withCookie('raw', binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error the byte-oriented matcher does not survive nesting in a combinator
  not(binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error nor in an and
  and(containing('a'), binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error nor in an or
  or(containing('a'), binaryEqualTo(new Uint8Array([1])));
  // @ts-expect-error nor inside the object form of matchesJsonPath
  matchingJsonPath('$.blob', binaryEqualTo(new Uint8Array([1])));

  // A raw document is a valid ContentMatcher and is not a matcher this package
  // built, which is what stops the guarantees above being sidestepped by an
  // object literal that happens to have the right fields.
  // @ts-expect-error a matcher comes from a builder, not from a literal
  get(anyUrl()).withHeader('Accept', { equalTo: 'application/json', schemaVersion: 'V7' });

  // A combinator over one matcher is 422 on both servers (deviation #27).
  and(containing('a'), containing('b'));
  // @ts-expect-error and needs at least two operands
  and(containing('a'));
  // @ts-expect-error so does or
  or(containing('a'));
}

/** The refusals that come from how a mapping is put together. */
export function malformedMappings(): void {
  // A path-parameter criterion needs a template to bind against, and every name
  // must be a variable that template binds. WireMock accepts the first and drops
  // the whole block, so an unsatisfiable criterion registers and the stub then
  // matches *every* request (deviation #54).
  get(urlPathTemplate('/orders/{id}')).withPathParam('id', matching('[0-9]+'));
  // @ts-expect-error the template binds no variable by that name
  get(urlPathTemplate('/orders/{id}')).withPathParam('orderId', matching('[0-9]+'));
  // @ts-expect-error there is no template to bind against
  get(urlPathEqualTo('/orders')).withPathParam('id', matching('[0-9]+'));
  // @ts-expect-error nor on a wildcard URL
  get(anyUrl()).withPathParam('id', matching('[0-9]+'));

  // A scenario state needs a scenario, which the server refuses without rather
  // than ignoring. This is the shape WireMock's own Java builder has: the state
  // methods live on what `inScenario` returns.
  get(anyUrl()).inScenario('flow').whenScenarioStateIs('Started').willSetStateTo('done');
  // @ts-expect-error requiredScenarioState needs a scenarioName
  get(anyUrl()).whenScenarioStateIs('Started');
  // @ts-expect-error newScenarioState needs a scenarioName
  get(anyUrl()).willSetStateTo('done');

  // Two URL criteria on one pattern are refused 422, and WireMock's echo
  // silently omits the ones it discarded (deviation #47). A verb takes one.
  // @ts-expect-error there is no second URL argument to give
  get(urlPathEqualTo('/a'), urlPathEqualTo('/b'));
}

/** The refusals that come from a response saying two things at once. */
export function overspecifiedResponses(): void {
  // Exactly one body form may be set. WireMock accepts the combination and
  // silently discards all but `body` (deviation #19).
  aResponse().withStatus(200).withJsonBody({ ok: true }).withStatus(201);
  // @ts-expect-error body and jsonBody are two body forms
  aResponse().withBody('hello').withJsonBody({ ok: true });
  // @ts-expect-error base64Body and bodyFileName are two body forms
  aResponse()
    .withBase64Body(new Uint8Array([1]))
    .withBodyFile('order.json');
  // @ts-expect-error the same body form twice is still two of them
  aResponse().withBody('a').withBody('b');

  // A fault replaces the response rather than decorating it, so it occupies the
  // same slot: a stub asking for both is stating two different intents.
  aResponse().withStatus(200).withFault('EMPTY_RESPONSE');
  // @ts-expect-error a fault cannot be combined with a body
  aResponse().withBody('hello').withFault('EMPTY_RESPONSE');
  // @ts-expect-error nor a body with a fault
  aResponse().withFault('EMPTY_RESPONSE').withJsonBody({ ok: true });

  // `response-template` is the only transformer the server recognises; any
  // other name is refused 422 with code 1004.
  aResponse().withTransformers('response-template');
  // @ts-expect-error an unrecognised transformer would silently do nothing
  aResponse().withTransformers('response-template', 'body-rewriter');

  // The fault names are the four the server implements.
  // @ts-expect-error not one of the four faults
  aResponse().withFault('CONNECTION_RESET');
}

/** The fields that are not here at all, because the server refuses them. */
export function absentVocabulary(): void {
  // Every one of these is a real WireMock feature the server answers 422 for,
  // naming the roadmap item it is waiting on. A name that does not resolve is
  // the same refusal, arrived at before the call was written.
  // @ts-expect-error equalToXml is deferred (XML matching)
  get(anyUrl()).withRequestBody(equalToXml('<a/>'));
  // @ts-expect-error matchesXPath is deferred (XPath matching)
  get(anyUrl()).withRequestBody(matchesXPath('//a'));
  // @ts-expect-error multipartPatterns is deferred
  get(anyUrl()).withMultipartRequestBody(containing('a'));
  // @ts-expect-error proxying is deferred
  aResponse().proxiedFrom('http://upstream.test');
  // @ts-expect-error webhooks are deferred
  get(anyUrl()).withPostServeAction('webhook', {});
  // @ts-expect-error `absent: false` is refused; not(absent()) is how presence is stated
  get(anyUrl()).withHeader('X-Trace', absent(false));
}
