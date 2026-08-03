// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { firstSyntaxError, parseJsonPointer, resolveJsonPointer } from './json-pointer';

/** The span a resolution names, as the text it covers — which is what a reader would see selected. */
function selected(text: string, pointer: string): string | undefined {
  const found = resolveJsonPointer(text, pointer);
  return found.resolved ? text.slice(found.from, found.to) : undefined;
}

const MAPPING = `{
  "request": {
    "method": "GET",
    "urlPath": "/api/orders",
    "bodyPatterns": [
      { "equalTo": "one" },
      { "matchesXPath": "//a" }
    ]
  },
  "response": {
    "status": 200
  }
}`;

describe('parseJsonPointer', () => {
  it('reads the empty pointer as the document itself', () => {
    expect(parseJsonPointer('')).toEqual([]);
  });

  it('refuses a string that does not begin with a slash', () => {
    expect(parseJsonPointer('request/method')).toBeUndefined();
  });

  it('decodes ~1 before ~0, which is what keeps a literal tilde-one out of a path', () => {
    // "~01" decodes to "~1" and must not then decode again into "/". Reversing
    // the two substitutions would silently split one token into two.
    expect(parseJsonPointer('/a~01b')).toEqual(['a~1b']);
    expect(parseJsonPointer('/a~1b')).toEqual(['a/b']);
    expect(parseJsonPointer('/a~0b')).toEqual(['a~b']);
  });

  it('keeps an empty token, which names a member whose key is the empty string', () => {
    expect(parseJsonPointer('/')).toEqual(['']);
  });
});

describe('resolveJsonPointer', () => {
  it('selects the whole property, name included, so the field the error named is covered', () => {
    expect(selected(MAPPING, '/request/method')).toBe('"method": "GET"');
  });

  it('walks into arrays by index', () => {
    expect(selected(MAPPING, '/request/bodyPatterns/1')).toBe('{ "matchesXPath": "//a" }');
    expect(selected(MAPPING, '/request/bodyPatterns/1/matchesXPath')).toBe('"matchesXPath": "//a"');
  });

  it('selects a nested object whole when the pointer stops at it', () => {
    const found = resolveJsonPointer(MAPPING, '/response');
    expect(found.resolved).toBe(true);
    expect(selected(MAPPING, '/response')).toMatch(/^"response": \{/);
  });

  it('resolves the empty pointer to the whole document', () => {
    expect(selected(MAPPING, '')).toBe(MAPPING);
  });

  it('reports a field that is not there, and how far it did get', () => {
    const found = resolveJsonPointer(MAPPING, '/request/multipartPatterns');
    expect(found).toEqual({
      resolved: false,
      reason: 'not-in-document',
      matched: '/request',
    });
  });

  it('reports a whole branch that is not there against the document root', () => {
    const found = resolveJsonPointer(MAPPING, '/metadata/team');
    expect(found).toEqual({ resolved: false, reason: 'not-in-document', matched: '' });
  });

  it('refuses an index past the end rather than choosing the nearest element', () => {
    const found = resolveJsonPointer(MAPPING, '/request/bodyPatterns/7');
    expect(found).toEqual({
      resolved: false,
      reason: 'not-in-document',
      matched: '/request/bodyPatterns',
    });
  });

  it('refuses a non-canonical array index, which RFC 6901 does not define', () => {
    expect(resolveJsonPointer(MAPPING, '/request/bodyPatterns/01').resolved).toBe(false);
    expect(resolveJsonPointer(MAPPING, '/request/bodyPatterns/-').resolved).toBe(false);
  });

  it('refuses to walk past a scalar', () => {
    const found = resolveJsonPointer(MAPPING, '/request/method/0');
    expect(found).toEqual({
      resolved: false,
      reason: 'not-in-document',
      matched: '/request/method',
    });
  });

  it('separates a pointer that is not one from a field that is missing', () => {
    const found = resolveJsonPointer(MAPPING, 'request');
    expect(found).toEqual({ resolved: false, reason: 'pointer-syntax', matched: '' });
  });

  it('finds a key containing a slash, which only the escaping makes reachable', () => {
    const text = '{\n  "headers": {\n    "Content-Type": { "equalTo": "text/plain" }\n  }\n}';
    expect(selected(text, '/headers/Content-Type/equalTo')).toBe('"equalTo": "text/plain"');
  });

  it('still resolves the fields that parse in a document being edited', () => {
    // A trailing comma and a half-typed key: invalid JSON, and exactly the state
    // a document is in while somebody is acting on a list of problems.
    const broken = '{\n  "request": { "method": "GET" },\n  "resp\n}';
    expect(selected(broken, '/request/method')).toBe('"method": "GET"');
  });

  it('names nothing in an empty document', () => {
    expect(resolveJsonPointer('', '/request')).toEqual({
      resolved: false,
      reason: 'not-in-document',
      matched: '',
    });
  });
});

describe('firstSyntaxError', () => {
  it('points at where a document stops being JSON', () => {
    const text = '{ "a": 1, }';
    const at = firstSyntaxError(text);
    expect(at).toBeDefined();
    if (at) {
      expect(at.from).toBeGreaterThanOrEqual(text.indexOf('}'));
    }
  });

  it('gives a span of at least one character, so a selection is visible', () => {
    const at = firstSyntaxError('{ "a": }');
    expect(at).toBeDefined();
    if (at) {
      // An error node is often zero-length, and an empty selection would be a
      // jump the reader cannot see land.
      expect(at.to).toBeGreaterThan(at.from);
    }
  });

  it('finds nothing in a document that parses', () => {
    expect(firstSyntaxError(MAPPING)).toBeUndefined();
  });
});
