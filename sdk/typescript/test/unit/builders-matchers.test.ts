// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
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
} from '../../src/index.js';

/**
 * The JSON each matcher emits.
 *
 * These are assertions against the shapes `api/openapi.yaml` defines, which is
 * the only place they can be checked without a server: a matcher that emits the
 * wrong field name is a 422 at registration, and a matcher that emits the right
 * field with the wrong *value* is a stub that registers and then matches the
 * wrong requests. The integration lane proves the server accepts what is built
 * here; this lane proves what is built is what was asked for.
 */
describe('the matcher vocabulary', () => {
  it('emits one key per matcher, so a document means the same on both servers', () => {
    // mockulus reads several matcher keys on one document as a conjunction and
    // WireMock honours only the first (deviation #26), so every builder here
    // emits exactly one — and `and` is how a conjunction is written.
    expect(equalTo('x')).toEqual({ equalTo: 'x' });
    expect(containing('js')).toEqual({ contains: 'js' });
    expect(notContaining('xml')).toEqual({ doesNotContain: 'xml' });
    expect(matching('[0-9]+')).toEqual({ matches: '[0-9]+' });
    expect(notMatching('[0-9]+')).toEqual({ doesNotMatch: '[0-9]+' });
    expect(absent()).toEqual({ absent: true });
  });

  it('folds case through equalTo rather than through a modifier of its own', () => {
    // `caseInsensitive` is read by `equalTo` and by nothing else, so it is
    // spelled as a second function rather than as a flag that could be written
    // beside a `contains` and do nothing.
    expect(equalToIgnoreCase('Bearer')).toEqual({ equalTo: 'Bearer', caseInsensitive: true });
  });

  describe('binaryEqualTo', () => {
    it('encodes bytes, so the caller never writes base64 by hand', () => {
      expect(binaryEqualTo(new Uint8Array([0x68, 0x69]))).toEqual({ binaryEqualTo: 'aGk=' });
    });

    it('takes an ArrayBuffer as the bytes it holds', () => {
      const buffer = new Uint8Array([0x00, 0xff]).buffer;
      expect(binaryEqualTo(buffer)).toEqual({ binaryEqualTo: 'AP8=' });
    });

    it('passes an already-encoded operand through, padded or not', () => {
      // Padding is optional in Java's decoder and so on the server, which is
      // what makes the unpadded spelling a Java encoder produces acceptable.
      expect(binaryEqualTo('aGk=')).toEqual({ binaryEqualTo: 'aGk=' });
      expect(binaryEqualTo('aGVsbG8')).toEqual({ binaryEqualTo: 'aGVsbG8' });
    });

    it('refuses a string that is not base64 at the call site', () => {
      expect(() => binaryEqualTo('not base64!')).toThrow(TypeError);
      // A trailing unit of one character cannot hold a byte, and is refused by
      // the padded and unpadded readings alike.
      expect(() => binaryEqualTo('aGVsbG')).not.toThrow();
      expect(() => binaryEqualTo('aGVsb')).toThrow(/not base64/);
    });
  });

  describe('equalToJson', () => {
    it('takes the document inline, which is the spelling a TypeScript caller has', () => {
      expect(equalToJson({ channel: 'web' })).toEqual({ equalToJson: { channel: 'web' } });
    });

    it('passes an escaped-string operand through unchanged', () => {
      // The form WireMock's own examples use. It reaches the server as a string
      // and is parsed there, which is why nothing here tries to re-encode it.
      expect(equalToJson('{"channel":"web"}')).toEqual({ equalToJson: '{"channel":"web"}' });
    });

    it('writes the two flags only when they were asked for', () => {
      expect(equalToJson({ a: 1 }, { ignoreExtraElements: true })).toEqual({
        equalToJson: { a: 1 },
        ignoreExtraElements: true,
      });
      expect(equalToJson({ a: 1 }, { ignoreArrayOrder: true, ignoreExtraElements: false })).toEqual(
        {
          equalToJson: { a: 1 },
          ignoreArrayOrder: true,
          ignoreExtraElements: false,
        },
      );
      // An empty options object leaves the document as bare as no options at
      // all: a flag nobody set is a flag the server never sees.
      expect(equalToJson({ a: 1 }, {})).toEqual({ equalToJson: { a: 1 } });
    });

    it('refuses undefined, which would serialise into an empty matcher', () => {
      expect(() => equalToJson(undefined)).toThrow(/undefined/);
    });

    it('carries json-unit placeholders as the values they are', () => {
      expect(
        equalToJson({
          id: JsonUnit.AnyString,
          seen: JsonUnit.Ignore,
          code: jsonUnitRegex('[A-Z]+'),
        }),
      ).toEqual({
        equalToJson: {
          id: '${json-unit.any-string}',
          seen: '${json-unit.ignore}',
          code: '${json-unit.regex}[A-Z]+',
        },
      });
    });

    it('spells every placeholder the server recognises', () => {
      expect(JsonUnit).toEqual({
        Ignore: '${json-unit.ignore}',
        IgnoreElement: '${json-unit.ignore-element}',
        AnyString: '${json-unit.any-string}',
        AnyNumber: '${json-unit.any-number}',
        AnyBoolean: '${json-unit.any-boolean}',
      });
    });
  });

  describe('the JSONPath matchers', () => {
    it('emits the bare string form when there is no inner matcher', () => {
      // The two spellings mean the same thing to the server, and the string is
      // the one a WireMock corpus is written in.
      expect(matchingJsonPath('$.customer.id')).toEqual({ matchesJsonPath: '$.customer.id' });
      expect(notMatchingJsonPath('$.legacy')).toEqual({ doesNotMatchJsonPath: '$.legacy' });
    });

    it('nests the inner matcher beside the expression', () => {
      expect(matchingJsonPath('$.customer.id', matching('[A-Z]{2}[0-9]{6}'))).toEqual({
        matchesJsonPath: { expression: '$.customer.id', matches: '[A-Z]{2}[0-9]{6}' },
      });
      expect(notMatchingJsonPath('$.status', equalTo('CANCELLED'))).toEqual({
        doesNotMatchJsonPath: { expression: '$.status', equalTo: 'CANCELLED' },
      });
    });
  });

  describe('matchingJsonSchema', () => {
    it('emits the schema alone under the default draft', () => {
      // The default is V202012 and the server fills nothing in, so a document
      // that does not name a draft is a document that does not name a draft.
      expect(matchingJsonSchema({ type: 'object' })).toEqual({
        matchesJsonSchema: { type: 'object' },
      });
    });

    it('carries the draft beside the matcher that reads it', () => {
      expect(
        matchingJsonSchema({ type: 'string', format: 'email' }, { schemaVersion: 'V7' }),
      ).toEqual({
        matchesJsonSchema: { type: 'string', format: 'email' },
        schemaVersion: 'V7',
      });
    });

    it('takes the schema as an escaped string too', () => {
      expect(matchingJsonSchema('{"type":"object"}')).toEqual({
        matchesJsonSchema: '{"type":"object"}',
      });
    });

    it('refuses undefined, which would serialise into an empty matcher', () => {
      expect(() => matchingJsonSchema(undefined)).toThrow(/undefined/);
    });
  });

  describe('the date-time matchers', () => {
    it('emits the operand under its own key', () => {
      expect(before('2021-06-14T12:00:00Z')).toEqual({ before: '2021-06-14T12:00:00Z' });
      expect(after('now')).toEqual({ after: 'now' });
      expect(equalToDateTime('2021-06-14')).toEqual({ equalToDateTime: '2021-06-14' });
    });

    it('carries the truncation and format modifiers beside the matcher', () => {
      expect(
        equalToDateTime('now +3 days', {
          truncateExpected: 'FIRST_DAY_OF_MONTH',
          truncateActual: 'FIRST_HOUR_OF_DAY',
          applyTruncationLast: true,
        }),
      ).toEqual({
        equalToDateTime: 'now +3 days',
        truncateExpected: 'FIRST_DAY_OF_MONTH',
        truncateActual: 'FIRST_HOUR_OF_DAY',
        applyTruncationLast: true,
      });
    });

    it('writes actualFormat, which replaces ISO parsing rather than extending it', () => {
      expect(before('2021-06-14T00:00:00Z', { actualFormat: 'dd/MM/yyyy' })).toEqual({
        before: '2021-06-14T00:00:00Z',
        actualFormat: 'dd/MM/yyyy',
      });
      expect(
        after('now', { actualFormat: 'unix', truncateActual: 'FIRST_SECOND_OF_MINUTE' }),
      ).toEqual({
        after: 'now',
        actualFormat: 'unix',
        truncateActual: 'FIRST_SECOND_OF_MINUTE',
      });
    });

    it('leaves out every modifier that was not asked for', () => {
      // An explicitly-undefined member is not the same as an absent one on the
      // wire: the server refuses `truncateActual: null` and ignores no key at
      // all, and only the document can tell the two apart.
      expect(after('now +1 hours', {})).toEqual({ after: 'now +1 hours' });
      expect(Object.keys(after('now'))).toEqual(['after']);
    });
  });

  describe('nowOffset', () => {
    it('writes the exact spelling the server parses, sign and all', () => {
      // `now+2days`, `now + 2 days`, a doubled space or a singular unit all
      // register on WireMock and then never match; this is the one spelling
      // that does not have to be got right by hand.
      expect(nowOffset(3, 'days')).toBe('now +3 days');
      expect(nowOffset(-15, 'minutes')).toBe('now -15 minutes');
      expect(nowOffset(0, 'seconds')).toBe('now +0 seconds');
    });

    it('refuses a fractional offset, which the server cannot read', () => {
      expect(() => nowOffset(1.5, 'hours')).toThrow(TypeError);
    });

    it('is usable as an operand that may carry an expected-side truncation', () => {
      expect(before(nowOffset(3, 'days'), { truncateExpected: 'FIRST_DAY_OF_MONTH' })).toEqual({
        before: 'now +3 days',
        truncateExpected: 'FIRST_DAY_OF_MONTH',
      });
    });
  });

  describe('the combinators', () => {
    it('nest the operands they were given', () => {
      expect(and(containing('a'), notContaining('b'))).toEqual({
        and: [{ contains: 'a' }, { doesNotContain: 'b' }],
      });
      expect(or(equalTo('a'), equalTo('b'), equalTo('c'))).toEqual({
        or: [{ equalTo: 'a' }, { equalTo: 'b' }, { equalTo: 'c' }],
      });
      expect(not(absent())).toEqual({ not: { absent: true } });
    });

    it('nest to any depth the server allows', () => {
      expect(not(and(containing('a'), or(equalTo('b'), equalTo('c'))))).toEqual({
        not: { and: [{ contains: 'a' }, { or: [{ equalTo: 'b' }, { equalTo: 'c' }] }] },
      });
    });
  });
});
