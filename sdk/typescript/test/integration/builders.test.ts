// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import {
  absent,
  after,
  and,
  anyUrl,
  aResponse,
  before,
  binaryEqualTo,
  containing,
  del,
  equalTo,
  equalToDateTime,
  equalToIgnoreCase,
  equalToJson,
  get,
  head,
  JsonUnit,
  jsonUnitRegex,
  matching,
  matchingJsonPath,
  matchingJsonSchema,
  MockulusClient,
  not,
  notContaining,
  notMatching,
  notMatchingJsonPath,
  nowOffset,
  options,
  or,
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
  type StubMapping,
} from '../../src/index.js';
import { startServer, type Server } from './harness.js';

/**
 * The builders against a real mockulus.
 *
 * The load-bearing claim of this layer is that **anything the builders can
 * express, the server accepts** — the fail-loud contract moved from a 422 at
 * registration to a name that does not resolve. A unit test cannot make that
 * claim: it can only say that a builder emits the JSON its author believed the
 * contract describes, which is exactly the belief that is worth doubting. So
 * every builder appears at least once in the sweep below, registered against a
 * live server, and the assertion is the status code.
 *
 * A 201 is a stronger answer here than it would be on WireMock, and that is the
 * point of the sweep. WireMock silently drops a key it does not recognise —
 * 201, and the key is absent from the stored mapping — so registration there
 * says nothing about whether a parameter name is real; three names that never
 * existed reached this project's spec and shipped code that way. mockulus
 * refuses an unknown key inside a matcher document, so a 201 means every key
 * that was written was read. The read-back comparison alongside it pins the
 * other half: the document that was registered is the document that is stored.
 *
 * Every stub lives under `/sdk-builders/<case>/…` and nothing here calls a
 * reset, which is the discipline SPEC §1 asks of anyone sharing a deployment.
 * The one stub that cannot be namespaced — `anyUrl()`, which is a wildcard by
 * definition — is parked in a scenario state its scenario is never in, so it
 * registers without ever matching a neighbour's traffic.
 */
describe('the builders against a live server', () => {
  let server: Server;
  let client: MockulusClient;

  /** Tags every stub this run creates, so a shared deployment could clean up by it. */
  const suite = `sdk-builders-${String(Date.now())}`;

  beforeAll(async () => {
    server = await startServer();
    client = new MockulusClient({ baseUrl: server.adminUrl });

    // Identity before anything is derived from it. Reachability is not
    // identity, and this repository has already paid once for a probe that
    // recorded answers from a process that merely happened to hold the port.
    const version = await client.system.version();
    expect(version.guessedWireMockVersion).toBe('3.x-subset');
  });

  afterAll(async () => {
    await server?.stop();
  });

  /**
   * Registers a mapping and insists on the 201.
   *
   * Written against `fetch` rather than against `client.mappings.create`
   * because the status is the assertion: the client raises a `MockulusError`
   * for anything that is not a 2xx, which would turn a 422 into a thrown error
   * whose message is about the client rather than about the field that was
   * refused. The error body is folded into the failure message here, so a
   * refusal names the JSON pointer that earned it.
   */
  async function register(mapping: StubMapping): Promise<StubMapping> {
    const response = await fetch(`${server.adminUrl}/__admin/mappings`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(mapping),
    });
    const text = await response.text();
    if (response.status !== 201) {
      throw new Error(
        `registration answered ${String(response.status)} rather than 201\n` +
          `  server said: ${text}\n` +
          `  mapping was: ${JSON.stringify(mapping)}`,
      );
    }
    return JSON.parse(text) as StubMapping;
  }

  /** A mapping tagged with the run, registered, with the echo checked against what was sent. */
  async function registerTagged(mapping: StubMapping): Promise<StubMapping> {
    const tagged: StubMapping = { ...mapping, metadata: { ...mapping.metadata, suite } };
    const stored = await register(tagged);
    // mockulus stores and echoes the document it was given, filling in no
    // defaults and canonicalising no spellings, so a difference here is the
    // server having rewritten something — which is the failure mode a
    // round-trip is the only way to see.
    expect(stored.request).toEqual(tagged.request);
    expect(stored.response).toEqual(tagged.response);
    return stored;
  }

  /**
   * The sweep: one entry per builder, or per combination whose acceptance is
   * not implied by the parts.
   *
   * The URL prefixes are not all distinct — several cases sit under one
   * prefix — because what is being asserted is registration rather than
   * matching, and two stubs on one URL is a legal thing to register.
   */
  const sweep: { name: string; mapping: StubMapping }[] = [
    {
      name: 'every named verb',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/verbs')).willReturn(aResponse().withStatus(200)),
      ),
    },
    { name: 'POST', mapping: stubFor(post(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    { name: 'PUT', mapping: stubFor(put(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    { name: 'PATCH', mapping: stubFor(patch(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    { name: 'DELETE', mapping: stubFor(del(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    { name: 'HEAD', mapping: stubFor(head(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    { name: 'OPTIONS', mapping: stubFor(options(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    { name: 'TRACE', mapping: stubFor(trace(urlPathEqualTo('/sdk-builders/sweep/verbs'))) },
    {
      name: 'a method WireMock does not enumerate',
      mapping: stubFor(request('PROPFIND', urlPathEqualTo('/sdk-builders/sweep/verbs'))),
    },
    {
      name: 'urlEqualTo, which is byte-exact over path and query',
      mapping: stubFor(get(urlEqualTo('/sdk-builders/sweep/url?page=1'))),
    },
    {
      name: 'urlMatching over path and query together',
      mapping: stubFor(get(urlMatching('/sdk-builders/sweep/url\\?page=[0-9]+'))),
    },
    { name: 'urlPathEqualTo', mapping: stubFor(get(urlPathEqualTo('/sdk-builders/sweep/url'))) },
    {
      name: 'urlPathMatching over the path alone',
      mapping: stubFor(get(urlPathMatching('/sdk-builders/sweep/url/[0-9]+'))),
    },
    {
      name: 'urlPathTemplate with a criterion per bound variable',
      mapping: stubFor(
        get(urlPathTemplate('/sdk-builders/sweep/{tenant}/orders/{id}'))
          .withPathParam('tenant', equalTo('acme'))
          .withPathParam('id', matching('[0-9]+')),
      ),
    },
    {
      name: 'anyUrl, parked in a state its scenario is never in',
      mapping: stubFor(
        get(anyUrl())
          // A wildcard cannot be namespaced by URL, so it is namespaced by
          // reachability instead: a scenario starts in `Started`, this stub
          // asks for a state nothing sets, and a stub that fails the state
          // check is treated as non-matching while iteration continues (§9.2).
          .inScenario(`${suite}-parked`)
          .whenScenarioStateIs('unreachable'),
      ),
    },
    {
      name: 'the string shorthand a verb takes for an exact URL',
      mapping: stubFor(get('/sdk-builders/sweep/shorthand')),
    },
    {
      name: 'criteria in every keyed block at once',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/keys'))
          .withHeader('Accept', containing('json'))
          .withHeader('X-Legacy', absent())
          .withHeader('X-Present', not(absent()))
          .withQueryParam('dryRun', matching('true|false'))
          .withCookie('session', equalTo('abc'))
          .withFormParam('channel', equalToIgnoreCase('WEB')),
      ),
    },
    {
      name: 'basic auth credentials',
      mapping: stubFor(get(urlPathEqualTo('/sdk-builders/sweep/auth')).withBasicAuth('a', 'b')),
    },
    {
      name: 'the string matchers as body patterns',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/strings'))
          .withRequestBody(containing('order'))
          .withRequestBody(notContaining('cancelled'))
          .withRequestBody(matching('.*web.*'))
          .withRequestBody(notMatching('.*legacy.*'))
          .withRequestBody(equalTo('order web')),
      ),
    },
    {
      name: 'binaryEqualTo, which is accepted only as a top-level body pattern',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/binary')).withRequestBody(
          binaryEqualTo(new Uint8Array([0x01, 0x02, 0x03])),
        ),
      ),
    },
    {
      name: 'equalToJson with both flags and every json-unit placeholder',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/json')).withRequestBody(
          equalToJson(
            {
              id: JsonUnit.AnyString,
              total: JsonUnit.AnyNumber,
              paid: JsonUnit.AnyBoolean,
              seen: JsonUnit.Ignore,
              optional: JsonUnit.IgnoreElement,
              code: jsonUnitRegex('[A-Z]{2}[0-9]{4}'),
            },
            { ignoreArrayOrder: true, ignoreExtraElements: true },
          ),
        ),
      ),
    },
    {
      name: 'equalToJson written as an escaped string, which is WireMock’s own spelling',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/json')).withRequestBody(
          equalToJson('{"channel":"web"}'),
        ),
      ),
    },
    {
      name: 'the JSONPath matchers, bare and nested, positive and negated',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/jsonpath'))
          .withRequestBody(matchingJsonPath('$.customer.id'))
          .withRequestBody(matchingJsonPath('$.customer.id', matching('[A-Z]{2}[0-9]{6}')))
          .withRequestBody(notMatchingJsonPath('$.legacy'))
          .withRequestBody(notMatchingJsonPath('$.status', equalTo('CANCELLED'))),
      ),
    },
    {
      name: 'matchesJsonSchema under the default draft',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/schema')).withRequestBody(
          matchingJsonSchema({ type: 'object', required: ['id'] }),
        ),
      ),
    },
    ...(['V4', 'V6', 'V7', 'V201909', 'V202012'] as const).map((schemaVersion) => ({
      name: `matchesJsonSchema under ${schemaVersion}`,
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/schema')).withRequestBody(
          matchingJsonSchema({ type: 'object' }, { schemaVersion }),
        ),
      ),
    })),
    {
      name: 'matchesJsonSchema written as an escaped string',
      mapping: stubFor(
        post(urlPathEqualTo('/sdk-builders/sweep/schema')).withRequestBody(
          matchingJsonSchema('{"type":"array"}'),
        ),
      ),
    },
    {
      name: 'the combinators, nested',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/combinators')).withHeader(
          'X-Channel',
          and(not(equalTo('legacy')), or(containing('web'), containing('mobile'))),
        ),
      ),
    },
    {
      name: 'a literal date-time operand carrying a zone',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/datetime')).withHeader(
          'X-When',
          before('2031-06-14T12:00:00Z'),
        ),
      ),
    },
    {
      name: 'a bare date, which equality widens to the whole day',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/datetime')).withQueryParam(
          'when',
          equalToDateTime('2021-06-14'),
        ),
      ),
    },
    {
      name: 'an RFC 1123 operand',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/datetime')).withHeader(
          'If-Modified-Since',
          after('Mon, 14 Jun 2021 12:00:00 GMT'),
        ),
      ),
    },
    {
      name: 'a now-relative operand with a truncation and an explicit ordering',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/datetime')).withHeader(
          'X-Window',
          before(nowOffset(3, 'days'), {
            truncateExpected: 'FIRST_DAY_OF_MONTH',
            applyTruncationLast: true,
          }),
        ),
      ),
    },
    {
      name: 'every truncation value, on the actual side beside a unix format',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/datetime')).withHeader(
          'X-Epoch',
          and(
            after('2021-01-01T00:00:00Z', {
              actualFormat: 'unix',
              truncateActual: 'FIRST_SECOND_OF_MINUTE',
            }),
            before('2031-01-01T00:00:00Z', {
              actualFormat: 'epoch',
              truncateActual: 'LAST_DAY_OF_YEAR',
            }),
          ),
        ),
      ),
    },
    {
      name: 'a Java date pattern as the actual format',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/datetime')).withHeader(
          'X-Filed',
          equalToDateTime('2021-06-14', { actualFormat: 'dd/MM/yyyy' }),
        ),
      ),
    },
    {
      name: 'the top-level fields, all of them at once',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/top-level'))
          .withName('every top-level field')
          .withId('9c47901d-6bd5-4b7a-8896-c0ac9b8d0b4e')
          .withPriority(3)
          .persistent()
          .withMetadata({ team: 'checkout' })
          .inScenario(`${suite}-top-level`)
          .whenScenarioStateIs('Started')
          .willSetStateTo('done'),
      ),
    },
    {
      name: 'the extremes of the priority range, which the server does not clamp',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/priority')).withPriority(-2147483648),
      ),
    },
    {
      name: 'a response with a status, a reason phrase and repeated headers',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/response')).willReturn(
          aResponse()
            .withStatus(203)
            .withStatusMessage('Non-Authoritative Information')
            .withHeader('Content-Type', 'application/json')
            .withHeader('Set-Cookie', 'a=1', 'b=2'),
        ),
      ),
    },
    {
      name: 'an inline text body',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/body')).willReturn(aResponse().withBody('hello')),
      ),
    },
    {
      name: 'an inline JSON body',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/body')).willReturn(
          aResponse().withJsonBody({ status: 'CREATED', items: [1, 2, 3] }),
        ),
      ),
    },
    {
      name: 'a base64 body, from bytes',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/body')).willReturn(
          aResponse().withBase64Body(new Uint8Array([0x00, 0x01, 0xff])),
        ),
      ),
    },
    {
      name: 'a body file, whose existence is deliberately not checked at registration',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/body')).willReturn(
          aResponse().withBodyFile('not-uploaded-yet.json'),
        ),
      ),
    },
    {
      name: 'response templating with a transformer parameter',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/template')).willReturn(
          aResponse()
            .withJsonBody({ path: '{{request.path}}', id: "{{randomValue type='UUID'}}" })
            .withTransformers('response-template')
            .withTransformerParameter('region', 'eu')
            .withTransformerParameters({ disableBodyTemplating: false }),
        ),
      ),
    },
    {
      name: 'a fixed delay beside a uniform distribution, which are summed',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/delay')).willReturn(
          aResponse().withFixedDelay(5).withUniformRandomDelay(0, 10),
        ),
      ),
    },
    {
      name: 'a log-normal distribution',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/delay')).willReturn(
          aResponse().withLogNormalRandomDelay(90, 0.4),
        ),
      ),
    },
    {
      name: 'a chunked dribble delay',
      mapping: stubFor(
        get(urlPathEqualTo('/sdk-builders/sweep/delay')).willReturn(
          aResponse().withBody('dribbled').withChunkedDribbleDelay(4, 40),
        ),
      ),
    },
    ...(
      [
        'CONNECTION_RESET_BY_PEER',
        'EMPTY_RESPONSE',
        'MALFORMED_RESPONSE_CHUNK',
        'RANDOM_DATA_THEN_CLOSE',
      ] as const
    ).map((fault) => ({
      name: `the ${fault} fault`,
      mapping: stubFor(
        get(urlPathEqualTo(`/sdk-builders/sweep/fault/${fault}`)).willReturn(
          aResponse().withFault(fault),
        ),
      ),
    })),
  ];

  describe('registration', () => {
    it.each(sweep)('accepts $name', async ({ mapping }) => {
      await registerTagged(mapping);
    });
  });

  describe('matching', () => {
    /** A mock-port request against this file's server. */
    function call(path: string, init?: RequestInit): Promise<Response> {
      return fetch(server.mockUrl + path, init);
    }

    it('matches on a header, a query parameter, a cookie and a form field at once', async () => {
      const url = '/sdk-builders/keys/order';
      await registerTagged(
        stubFor(
          post(urlPathEqualTo(url))
            .withHeader('Accept', containing('json'))
            .withQueryParam('dryRun', matching('true|false'))
            .withCookie('session', equalTo('abc'))
            .withFormParam('channel', equalToIgnoreCase('WEB'))
            .willReturn(aResponse().withStatus(200).withBody('all four')),
        ),
      );

      const matched = await call(`${url}?dryRun=true`, {
        method: 'POST',
        headers: { Accept: 'application/json', Cookie: 'session=abc' },
        body: new URLSearchParams({ channel: 'web' }),
      });
      expect(matched.status).toBe(200);
      expect(await matched.text()).toBe('all four');

      // One criterion changed is enough to stop matching, which is what says
      // the criteria are being evaluated rather than merely stored: a header
      // name is case-insensitive, but its value is not unless the matcher folds
      // case, and `Accept` here does not.
      const missed = await call(`${url}?dryRun=true`, {
        method: 'POST',
        headers: { Accept: 'application/xml', Cookie: 'session=abc' },
        body: new URLSearchParams({ channel: 'web' }),
      });
      expect(missed.status).toBe(404);
    });

    it('tells an absent header from a present one', async () => {
      const url = '/sdk-builders/absent/probe';
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            .withHeader('X-Legacy', absent())
            .willReturn(aResponse().withBody('absent')),
        ),
      );
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            // `{"absent": false}` is refused by the server and is not a shape
            // these builders can produce, so this is how presence is stated.
            .withHeader('X-Legacy', not(absent()))
            .willReturn(aResponse().withBody('present')),
        ),
      );

      expect(await (await call(url)).text()).toBe('absent');
      expect(await (await call(url, { headers: { 'X-Legacy': 'yes' } })).text()).toBe('present');
    });

    it('binds a path template variable and holds it to its criterion', async () => {
      await registerTagged(
        stubFor(
          get(urlPathTemplate('/sdk-builders/template/orders/{id}'))
            .withPathParam('id', matching('[0-9]+'))
            .willReturn(aResponse().withBody('numeric id')),
        ),
      );

      expect((await call('/sdk-builders/template/orders/42')).status).toBe(200);
      // The criterion on the bound variable is what does the work here: the
      // template itself matches either path. On WireMock the whole
      // `pathParameters` block would have been dropped and both would match.
      expect((await call('/sdk-builders/template/orders/abc')).status).toBe(404);
    });

    it('matches a JSON body structurally, through placeholders and extra members', async () => {
      const url = '/sdk-builders/json/order';
      await registerTagged(
        stubFor(
          post(urlPathEqualTo(url))
            .withRequestBody(
              equalToJson(
                { id: JsonUnit.AnyString, code: jsonUnitRegex('[A-Z]{2}[0-9]{4}') },
                { ignoreExtraElements: true },
              ),
            )
            .willReturn(aResponse().withBody('structural')),
        ),
      );

      const matched = await call(url, {
        method: 'POST',
        body: JSON.stringify({ id: 'x', code: 'GB1234', extra: true }),
      });
      expect(await matched.text()).toBe('structural');

      // The regex placeholder is a full match, so a code that merely contains
      // the pattern does not satisfy it.
      const missed = await call(url, {
        method: 'POST',
        body: JSON.stringify({ id: 'x', code: 'GB12345' }),
      });
      expect(missed.status).toBe(404);
    });

    it('applies an inner matcher to the values a JSONPath selects', async () => {
      const url = '/sdk-builders/jsonpath/order';
      await registerTagged(
        stubFor(
          post(urlPathEqualTo(url))
            .withRequestBody(matchingJsonPath('$.customer.id', matching('[A-Z]{2}[0-9]{6}')))
            .withRequestBody(notMatchingJsonPath('$.status', equalTo('CANCELLED')))
            .willReturn(aResponse().withBody('jsonpath')),
        ),
      );

      const matched = await call(url, {
        method: 'POST',
        body: JSON.stringify({ customer: { id: 'GB123456' }, status: 'OPEN' }),
      });
      expect(await matched.text()).toBe('jsonpath');

      const missed = await call(url, {
        method: 'POST',
        body: JSON.stringify({ customer: { id: 'GB123456' }, status: 'CANCELLED' }),
      });
      expect(missed.status).toBe(404);
    });

    it('validates a body against an embedded JSON Schema', async () => {
      const url = '/sdk-builders/schema/order';
      await registerTagged(
        stubFor(
          post(urlPathEqualTo(url))
            .withRequestBody(
              matchingJsonSchema(
                {
                  type: 'object',
                  required: ['id'],
                  properties: { id: { type: 'string' }, total: { type: 'number' } },
                },
                { schemaVersion: 'V7' },
              ),
            )
            .willReturn(aResponse().withBody('valid')),
        ),
      );

      const matched = await call(url, {
        method: 'POST',
        body: JSON.stringify({ id: 'a', total: 3 }),
      });
      expect(await matched.text()).toBe('valid');

      // A subject that fails the schema is a plain non-match, and so is one
      // that is not JSON at all — where WireMock falls back to validating the
      // raw text as a JSON string (deviation #55).
      expect((await call(url, { method: 'POST', body: JSON.stringify({ total: 3 }) })).status).toBe(
        404,
      );
      expect((await call(url, { method: 'POST', body: 'not json' })).status).toBe(404);
    });

    it('reads a bare date as the whole day it names', async () => {
      // The widening this project makes over WireMock, which reads a date-only
      // operand as midnight and so excludes almost every moment of the day
      // (deviation #51). It is confined to equality on purpose.
      const url = '/sdk-builders/datetime/day';
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            .withQueryParam('when', equalToDateTime('2021-06-14'))
            .willReturn(aResponse().withBody('that day')),
        ),
      );

      expect(await (await call(`${url}?when=2021-06-14T15:30:00Z`)).text()).toBe('that day');
      expect((await call(`${url}?when=2021-06-15T00:00:00Z`)).status).toBe(404);
    });

    it('reads a numeric value through actualFormat rather than as ISO-8601', async () => {
      const url = '/sdk-builders/datetime/epoch';
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            // 1623672000 is 2021-06-14T12:00:00Z in seconds.
            .withHeader('X-Epoch', after('2021-01-01T00:00:00Z', { actualFormat: 'unix' }))
            .willReturn(aResponse().withBody('after new year')),
        ),
      );

      const matched = await call(url, { headers: { 'X-Epoch': '1623672000' } });
      expect(await matched.text()).toBe('after new year');
      expect((await call(url, { headers: { 'X-Epoch': '1000000000' } })).status).toBe(404);
      // A non-numeric value under `unix` is a plain non-match. WireMock's parse
      // is an unguarded `Long.parseLong`, so the same request reaches the client
      // as a 500 error page there (deviation #52).
      expect((await call(url, { headers: { 'X-Epoch': 'banana' } })).status).toBe(404);
    });

    it('compares raw bytes through a binaryEqualTo body pattern', async () => {
      const url = '/sdk-builders/binary/upload';
      const payload = new Uint8Array([0x00, 0x01, 0x02, 0xff]);
      await registerTagged(
        stubFor(
          post(urlPathEqualTo(url))
            .withRequestBody(binaryEqualTo(payload))
            .willReturn(aResponse().withBody('bytes')),
        ),
      );

      expect(await (await call(url, { method: 'POST', body: payload })).text()).toBe('bytes');
      expect(
        (await call(url, { method: 'POST', body: new Uint8Array([0x00, 0x01, 0x02, 0xfe]) }))
          .status,
      ).toBe(404);
    });

    it('drives a scenario from one state to the next', async () => {
      const url = '/sdk-builders/scenario/order';
      const scenario = `${suite}-checkout`;
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            .inScenario(scenario)
            .whenScenarioStateIs('Started')
            .willSetStateTo('placed')
            .willReturn(aResponse().withBody('first')),
        ),
      );
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            .inScenario(scenario)
            .whenScenarioStateIs('placed')
            .willReturn(aResponse().withBody('second')),
        ),
      );

      expect(await (await call(url)).text()).toBe('first');
      expect(await (await call(url)).text()).toBe('second');
      expect(await (await call(url)).text()).toBe('second');
    });

    it('selects the lower priority among two stubs that both match', async () => {
      const url = '/sdk-builders/priority/order';
      await registerTagged(
        stubFor(get(urlPathEqualTo(url)).willReturn(aResponse().withBody('default priority'))),
      );
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url)).withPriority(1).willReturn(aResponse().withBody('priority one')),
        ),
      );

      expect(await (await call(url)).text()).toBe('priority one');
    });

    it('matches basic auth credentials as the Authorization header they stand for', async () => {
      const url = '/sdk-builders/auth/order';
      await registerTagged(
        stubFor(
          get(urlPathEqualTo(url))
            .withBasicAuth('alice', 'sesame')
            .willReturn(aResponse().withBody('authorised')),
        ),
      );

      const credential = `Basic ${btoa('alice:sesame')}`;
      expect(await (await call(url, { headers: { Authorization: credential } })).text()).toBe(
        'authorised',
      );
      expect((await call(url)).status).toBe(404);
    });

    it('serves each body form as the bytes it names', async () => {
      await registerTagged(
        stubFor(
          get(urlPathEqualTo('/sdk-builders/body/json')).willReturn(
            aResponse()
              .withStatus(201)
              .withHeader('Content-Type', 'application/json')
              .withJsonBody({ status: 'CREATED' }),
          ),
        ),
      );
      await registerTagged(
        stubFor(
          get(urlPathEqualTo('/sdk-builders/body/bytes')).willReturn(
            aResponse().withBase64Body(new Uint8Array([0x00, 0x01, 0xff])),
          ),
        ),
      );

      const json = await call('/sdk-builders/body/json');
      expect(json.status).toBe(201);
      expect(json.headers.get('content-type')).toBe('application/json');
      expect(await json.json()).toEqual({ status: 'CREATED' });

      const bytes = await call('/sdk-builders/body/bytes');
      expect(new Uint8Array(await bytes.arrayBuffer())).toEqual(new Uint8Array([0x00, 0x01, 0xff]));
    });

    it('renders a templated response when the stub asks for the transformer', async () => {
      await registerTagged(
        stubFor(
          get(urlPathEqualTo('/sdk-builders/template/render')).willReturn(
            aResponse()
              .withBody('{{request.path}} in {{parameters.region}}')
              .withTransformers('response-template')
              .withTransformerParameter('region', 'eu'),
          ),
        ),
      );
      await registerTagged(
        stubFor(
          get(urlPathEqualTo('/sdk-builders/template/literal')).willReturn(
            // Without the transformer the braces are served literally, which is
            // what the pinned WireMock does and what makes a `{{` in mock data
            // safe by default.
            aResponse().withBody('{{request.path}}'),
          ),
        ),
      );

      expect(await (await call('/sdk-builders/template/render')).text()).toBe(
        '/sdk-builders/template/render in eu',
      );
      expect(await (await call('/sdk-builders/template/literal')).text()).toBe('{{request.path}}');
    });

    it('waits for the delay it was given before answering', async () => {
      const url = '/sdk-builders/delay/slow';
      await registerTagged(
        stubFor(get(urlPathEqualTo(url)).willReturn(aResponse().withFixedDelay(200))),
      );

      const started = Date.now();
      expect((await call(url)).status).toBe(200);
      // The floor is the assertion, and it is below the delay only to leave
      // room for a coarse clock. A machine slow enough to overshoot is not a
      // failure; one that answers early has ignored the field.
      expect(Date.now() - started).toBeGreaterThanOrEqual(180);
    });

    it('breaks the connection when the stub asks for a fault', async () => {
      const url = '/sdk-builders/fault/empty';
      await registerTagged(
        stubFor(get(urlPathEqualTo(url)).willReturn(aResponse().withFault('EMPTY_RESPONSE'))),
      );

      // A fault replaces the response rather than decorating it: the connection
      // closes with nothing written, which reaches a client as a transport
      // failure rather than as a status.
      await expect(call(url)).rejects.toThrow();
    });

    it('finds its own stubs by the metadata tag every one of them carries', async () => {
      // The same matcher vocabulary, in a fourth position — which is the point
      // of there being one: a matcher that works in `bodyPatterns` works here
      // unchanged. Tagging on the way in and cleaning up by the tag is the
      // discipline SPEC §1 asks of a suite sharing a deployment, and it is what
      // this file does instead of a reset.
      const found = await client.mappings.findByMetadata(
        equalToJson({ suite }, { ignoreExtraElements: true }),
      );
      expect(found.mappings.length).toBeGreaterThan(sweep.length);
      for (const mapping of found.mappings) {
        expect(mapping.metadata).toMatchObject({ suite });
      }
    });
  });
});
