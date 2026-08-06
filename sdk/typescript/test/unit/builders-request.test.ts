// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  absent,
  any,
  anyUrl,
  aResponse,
  binaryEqualTo,
  containing,
  del,
  equalTo,
  get,
  head,
  matching,
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
} from '../../src/index.js';

/**
 * The mapping document a builder chain produces.
 *
 * The worked example in the SOW is the specification of the call shape, and the
 * first case below is that example: if what it emits ever stops being the
 * document in `api/openapi.yaml`'s own `StubMapping` example, one of the two is
 * wrong and this is where it shows.
 */
describe('the mapping builder', () => {
  it('builds the worked example exactly', () => {
    const suite = 'checkout-smoke';
    expect(
      stubFor(
        get(urlPathEqualTo('/x'))
          .withHeader('Accept', containing('json'))
          .inScenario('flow')
          .whenScenarioStateIs('Started')
          .willSetStateTo('done')
          .withPriority(3)
          .withMetadata({ suite })
          .willReturn(
            aResponse()
              .withStatus(201)
              .withJsonBody({ orderId: 1 })
              .withTransformers('response-template')
              .withFixedDelay(40),
          ),
      ),
    ).toEqual({
      request: { method: 'GET', urlPath: '/x', headers: { Accept: { contains: 'json' } } },
      scenarioName: 'flow',
      requiredScenarioState: 'Started',
      newScenarioState: 'done',
      priority: 3,
      metadata: { suite: 'checkout-smoke' },
      response: {
        status: 201,
        jsonBody: { orderId: 1 },
        transformers: ['response-template'],
        fixedDelayMilliseconds: 40,
      },
    });
  });

  describe('the verbs', () => {
    it('write the method they name', () => {
      const url = urlPathEqualTo('/v');
      expect(stubFor(get(url)).request?.method).toBe('GET');
      expect(stubFor(post(url)).request?.method).toBe('POST');
      expect(stubFor(put(url)).request?.method).toBe('PUT');
      expect(stubFor(patch(url)).request?.method).toBe('PATCH');
      expect(stubFor(del(url)).request?.method).toBe('DELETE');
      expect(stubFor(head(url)).request?.method).toBe('HEAD');
      expect(stubFor(options(url)).request?.method).toBe('OPTIONS');
      expect(stubFor(trace(url)).request?.method).toBe('TRACE');
      expect(stubFor(any(url)).request?.method).toBe('ANY');
    });

    it('take a method the named verbs do not cover', () => {
      // Any method name is accepted, including one WireMock's own enumeration
      // does not define, because a mock server exists to stand in for services
      // that have them.
      expect(stubFor(request('PROPFIND', urlPathEqualTo('/v'))).request?.method).toBe('PROPFIND');
    });

    it('read a bare string as an exact-URL criterion, as WireMock does', () => {
      expect(stubFor(get('/orders?page=1')).request).toEqual({
        method: 'GET',
        url: '/orders?page=1',
      });
    });
  });

  describe('the URL criteria', () => {
    it('write exactly one field each', () => {
      expect(stubFor(get(urlEqualTo('/a?b=1'))).request).toEqual({ method: 'GET', url: '/a?b=1' });
      expect(stubFor(get(urlMatching('/a/[0-9]+'))).request).toEqual({
        method: 'GET',
        urlPattern: '/a/[0-9]+',
      });
      expect(stubFor(get(urlPathEqualTo('/a'))).request).toEqual({ method: 'GET', urlPath: '/a' });
      expect(stubFor(get(urlPathMatching('/a/[0-9]+'))).request).toEqual({
        method: 'GET',
        urlPathPattern: '/a/[0-9]+',
      });
      expect(stubFor(get(urlPathTemplate('/orders/{id}'))).request).toEqual({
        method: 'GET',
        urlPathTemplate: '/orders/{id}',
      });
    });

    it('write no URL field at all for anyUrl', () => {
      // A wildcard is the absence of a criterion, not a criterion that matches
      // everything: the field is not in the document.
      expect(stubFor(get(anyUrl())).request).toEqual({ method: 'GET' });
    });

    it('refuse a path that could never match, at the call site', () => {
      expect(() => urlEqualTo('orders')).toThrow(/starting with \//);
      expect(() => urlPathEqualTo('orders')).toThrow(/starting with \//);
      // A regular expression legitimately starts with an anchor or a group, so
      // the pattern forms are not held to it.
      expect(() => urlMatching('^/orders$')).not.toThrow();
      expect(() => urlPathMatching('(/a|/b)')).not.toThrow();
    });

    it('refuse a template the server would refuse', () => {
      // Checked here because the compile-time path-parameter names are read out
      // of this literal: a template the server reads differently would make the
      // set the compiler enforces the wrong set.
      expect(() => urlPathTemplate('orders/{id}')).toThrow(/starting with \//);
      expect(() => urlPathTemplate('/orders/{}')).toThrow(/malformed variable/);
      expect(() => urlPathTemplate('/orders/{id}/items/{id}')).toThrow(/more than once/);
      expect(() => urlPathTemplate('/orders/x{id}')).toThrow(/whole segment/);
    });
  });

  describe('the keyed criteria', () => {
    it('accumulate under the block each belongs to', () => {
      expect(
        stubFor(
          post(urlPathEqualTo('/orders'))
            .withHeader('Accept', containing('json'))
            .withHeader('X-Legacy', absent())
            .withQueryParam('dryRun', matching('true|false'))
            .withCookie('session', equalTo('abc'))
            .withFormParam('channel', equalTo('web')),
        ).request,
      ).toEqual({
        method: 'POST',
        urlPath: '/orders',
        headers: { Accept: { contains: 'json' }, 'X-Legacy': { absent: true } },
        queryParameters: { dryRun: { matches: 'true|false' } },
        cookies: { session: { equalTo: 'abc' } },
        formParameters: { channel: { equalTo: 'web' } },
      });
    });

    it('let a later criterion on the same key replace an earlier one', () => {
      // One name, one criterion: two entries under one key is not a shape the
      // document has, so the last one written is the one that means something.
      expect(
        stubFor(
          get(anyUrl()).withHeader('Accept', containing('json')).withHeader('Accept', absent()),
        ).request?.headers,
      ).toEqual({ Accept: { absent: true } });
    });

    it('bind a path parameter to a variable the template declares', () => {
      expect(
        stubFor(
          get(urlPathTemplate('/tenants/{tenant}/orders/{id}'))
            .withPathParam('tenant', equalTo('acme'))
            .withPathParam('id', matching('[0-9]+')),
        ).request,
      ).toEqual({
        method: 'GET',
        urlPathTemplate: '/tenants/{tenant}/orders/{id}',
        pathParameters: { tenant: { equalTo: 'acme' }, id: { matches: '[0-9]+' } },
      });
    });
  });

  describe('the body patterns', () => {
    it('accumulate in the order they were written', () => {
      expect(
        stubFor(
          post(anyUrl()).withRequestBody(containing('order')).withRequestBody(matching('.*web.*')),
        ).request?.bodyPatterns,
      ).toEqual([{ contains: 'order' }, { matches: '.*web.*' }]);
    });

    it('take the byte-oriented matcher, which no other position does', () => {
      expect(
        stubFor(post(anyUrl()).withRequestBody(binaryEqualTo(new Uint8Array([1, 2, 3])))).request
          ?.bodyPatterns,
      ).toEqual([{ binaryEqualTo: 'AQID' }]);
    });
  });

  describe('the top-level fields', () => {
    it('write basic auth as the credential pair the server expects', () => {
      expect(stubFor(get(anyUrl()).withBasicAuth('alice', 'sesame')).request).toEqual({
        method: 'GET',
        basicAuthCredentials: { username: 'alice', password: 'sesame' },
      });
    });

    it('write a name, an id, a priority and persistence', () => {
      const id = '9c47901d-6bd5-4b7a-8896-c0ac9b8d0b4e';
      expect(
        stubFor(get(anyUrl()).withName('the happy path').withId(id).withPriority(-3).persistent()),
      ).toEqual({
        request: { method: 'GET' },
        name: 'the happy path',
        id,
        priority: -3,
        persistent: true,
      });
    });

    it('write persistence as false when it is asked for explicitly', () => {
      expect(stubFor(get(anyUrl()).persistent(false)).persistent).toBe(false);
    });

    it('merge metadata rather than replacing it', () => {
      // A suite tags its stubs on the way in and cleans up by that tag; a
      // second call adding a field must not take the tag with it.
      expect(
        stubFor(get(anyUrl()).withMetadata({ suite: 'smoke' }).withMetadata({ team: 'checkout' }))
          .metadata,
      ).toEqual({ suite: 'smoke', team: 'checkout' });
    });

    it('refuse an id that is not the canonical spelling', () => {
      // The 24-character base64 encoding of the raw bytes is one WireMock
      // accepts and silently rewrites; refusing it is what stops a client
      // being handed back an id it did not choose (deviation #24).
      expect(() => get(anyUrl()).withId('9c47901d6bd54b7a8896c0ac9b8d0b4e')).toThrow(/UUID/);
      expect(() => get(anyUrl()).withId('nkeQHWvVS3qIlsCsm40LTg')).toThrow(/UUID/);
      expect(() => get(anyUrl()).withId('urn:uuid:9c47901d-6bd5-4b7a-8896-c0ac9b8d0b4e')).toThrow(
        /UUID/,
      );
    });

    it('refuse a priority outside the range the field is declared over', () => {
      expect(() => get(anyUrl()).withPriority(1.5)).toThrow(RangeError);
      expect(() => get(anyUrl()).withPriority(2 ** 31)).toThrow(RangeError);
      // No clamping and no narrowing: the whole signed 32-bit range is legal,
      // which is what the pinned WireMock accepts.
      expect(stubFor(get(anyUrl()).withPriority(2147483647)).priority).toBe(2147483647);
    });
  });

  describe('the scenario fields', () => {
    it('write the name and both states', () => {
      expect(
        stubFor(
          get(anyUrl())
            .inScenario('order-flow')
            .whenScenarioStateIs('Started')
            .willSetStateTo('done'),
        ),
      ).toEqual({
        request: { method: 'GET' },
        scenarioName: 'order-flow',
        requiredScenarioState: 'Started',
        newScenarioState: 'done',
      });
    });

    it('write a scenario with neither state, which is a stub that always matches in it', () => {
      expect(stubFor(get(anyUrl()).inScenario('order-flow')).scenarioName).toBe('order-flow');
      expect(stubFor(get(anyUrl()).inScenario('order-flow')).requiredScenarioState).toBeUndefined();
    });
  });

  it('leaves a builder untouched when a later call specialises it', () => {
    // The builder is immutable, which WireMock's Java one is not. A shared base
    // that grew a body because a sibling case added one would be a bug found in
    // the sibling, so the two answers below have to differ.
    const base = get(urlPathEqualTo('/shared'));
    const withHeader = base.withHeader('Accept', containing('json'));
    expect(stubFor(base).request).toEqual({ method: 'GET', urlPath: '/shared' });
    expect(stubFor(withHeader).request?.headers).toEqual({ Accept: { contains: 'json' } });
  });

  it('emits a mapping with no response when none was asked for', () => {
    // A stub with no response serves 200 with an empty body, and the server
    // fills nothing in, so the document says nothing about it either.
    expect(stubFor(get(urlPathEqualTo('/bare')))).toEqual({
      request: { method: 'GET', urlPath: '/bare' },
    });
  });
});
