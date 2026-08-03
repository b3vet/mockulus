// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { anyUrl, aResponse, get, ResponseBuilder, stubFor } from '../../src/index.js';

/** The response document a builder chain produces. */
describe('the response builder', () => {
  /**
   * The response half of a mapping the chain produced, which is what the server
   * reads. The parameter admits both body states because that is what
   * `willReturn` admits: the distinction exists to stop a *second* body form
   * being written, and by the time a response is handed over it has done its
   * work.
   */
  function responseOf(response: ResponseBuilder<'unset' | 'set'>): unknown {
    return stubFor(get(anyUrl()).willReturn(response)).response;
  }

  it('writes the status and the reason phrase', () => {
    expect(responseOf(aResponse().withStatus(503).withStatusMessage('Bad Gateway'))).toEqual({
      status: 503,
      statusMessage: 'Bad Gateway',
    });
  });

  it('refuses a status the server would refuse, at the call site', () => {
    // WireMock writes a positive out-of-range status unvalidated, producing a
    // malformed status line on the wire, and treats a non-positive one as
    // unset. Both are a `RangeError` here rather than a 422 there.
    expect(() => aResponse().withStatus(0)).toThrow(RangeError);
    expect(() => aResponse().withStatus(99)).toThrow(RangeError);
    expect(() => aResponse().withStatus(600)).toThrow(RangeError);
    expect(() => aResponse().withStatus(200.5)).toThrow(RangeError);
  });

  describe('headers', () => {
    it('write one value as a string and several as an array', () => {
      // A one-element array is stored and echoed as an array where WireMock
      // collapses it to a string, which is a difference in the document and not
      // on the wire (deviation #44) — so a single value is written as a string.
      expect(responseOf(aResponse().withHeader('Content-Type', 'application/json'))).toEqual({
        headers: { 'Content-Type': 'application/json' },
      });
      expect(responseOf(aResponse().withHeader('Set-Cookie', 'a=1', 'b=2'))).toEqual({
        headers: { 'Set-Cookie': ['a=1', 'b=2'] },
      });
    });

    it('accumulate across calls', () => {
      expect(responseOf(aResponse().withHeader('X-One', '1').withHeader('X-Two', '2'))).toEqual({
        headers: { 'X-One': '1', 'X-Two': '2' },
      });
    });
  });

  describe('the body forms', () => {
    it('write each under its own field', () => {
      expect(responseOf(aResponse().withBody('hello'))).toEqual({ body: 'hello' });
      expect(responseOf(aResponse().withJsonBody({ ok: true }))).toEqual({
        jsonBody: { ok: true },
      });
      expect(responseOf(aResponse().withBase64Body(new Uint8Array([0x68, 0x69])))).toEqual({
        base64Body: 'aGk=',
      });
      expect(responseOf(aResponse().withBodyFile('order.json'))).toEqual({
        bodyFileName: 'order.json',
      });
    });

    it('take an already-encoded base64 body, and refuse one that is not', () => {
      expect(responseOf(aResponse().withBase64Body('aGVsbG8'))).toEqual({ base64Body: 'aGVsbG8' });
      expect(() => aResponse().withBase64Body('nope!')).toThrow(/not base64/);
    });

    it('refuse an undefined JSON body, which would serialise into no body at all', () => {
      expect(() => aResponse().withJsonBody(undefined)).toThrow(/undefined/);
    });

    it('refuse an empty body file name, which could never resolve', () => {
      expect(() => aResponse().withBodyFile('')).toThrow(TypeError);
    });

    it('writes a fault, which replaces the response rather than decorating it', () => {
      expect(responseOf(aResponse().withFault('CONNECTION_RESET_BY_PEER'))).toEqual({
        fault: 'CONNECTION_RESET_BY_PEER',
      });
    });
  });

  describe('the delays', () => {
    it('write a fixed delay', () => {
      expect(responseOf(aResponse().withFixedDelay(40))).toEqual({ fixedDelayMilliseconds: 40 });
    });

    it('write each distribution under its discriminator', () => {
      expect(responseOf(aResponse().withUniformRandomDelay(10, 50))).toEqual({
        delayDistribution: { type: 'uniform', lower: 10, upper: 50 },
      });
      expect(responseOf(aResponse().withLogNormalRandomDelay(90, 0.4))).toEqual({
        delayDistribution: { type: 'lognormal', median: 90, sigma: 0.4 },
      });
    });

    it('write a chunked dribble delay', () => {
      expect(responseOf(aResponse().withChunkedDribbleDelay(5, 500))).toEqual({
        chunkedDribbleDelay: { numberOfChunks: 5, totalDuration: 500 },
      });
    });

    it('sum with the fixed delay rather than replacing it, so both may be set', () => {
      expect(responseOf(aResponse().withFixedDelay(20).withUniformRandomDelay(0, 10))).toEqual({
        fixedDelayMilliseconds: 20,
        delayDistribution: { type: 'uniform', lower: 0, upper: 10 },
      });
    });

    it('refuse the values the server refuses, at the call site', () => {
      // The server refuses a negative or fractional delay 422 rather than
      // normalising it (deviation #36), so rounding here would be this package
      // quietly serving a different stub than the one that was written.
      expect(() => aResponse().withFixedDelay(-1)).toThrow(RangeError);
      expect(() => aResponse().withFixedDelay(1.5)).toThrow(RangeError);
      expect(() => aResponse().withUniformRandomDelay(50, 10)).toThrow(/upper bound/);
      expect(() => aResponse().withLogNormalRandomDelay(90, -1)).toThrow(RangeError);
      expect(() => aResponse().withChunkedDribbleDelay(0, 100)).toThrow(/at least one chunk/);
      expect(() => aResponse().withChunkedDribbleDelay(2, -1)).toThrow(RangeError);
    });
  });

  describe('templating', () => {
    it('writes the one transformer the server recognises', () => {
      expect(responseOf(aResponse().withTransformers('response-template'))).toEqual({
        transformers: ['response-template'],
      });
    });

    it('accumulates transformer parameters, singly and in blocks', () => {
      expect(
        responseOf(
          aResponse()
            .withTransformerParameter('region', 'eu')
            .withTransformerParameters({ disableBodyTemplating: true }),
        ),
      ).toEqual({ transformerParameters: { region: 'eu', disableBodyTemplating: true } });
    });
  });

  it('leaves a builder untouched when a later call specialises it', () => {
    const base = aResponse().withStatus(200);
    const json = base.withJsonBody({ ok: true });
    expect(responseOf(base)).toEqual({ status: 200 });
    expect(responseOf(json)).toEqual({ status: 200, jsonBody: { ok: true } });
  });
});
