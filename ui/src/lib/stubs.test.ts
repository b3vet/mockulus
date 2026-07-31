// SPDX-License-Identifier: Apache-2.0
import type { StubMapping } from '@mockulus/admin-sdk';
import { describe, expect, it } from 'vitest';
import {
  PAGE_SIZE,
  filterMappings,
  methodOf,
  methodsPresent,
  pageCount,
  pageOf,
  pageRange,
  stubIdOf,
  urlCriterionOf,
} from './stubs';
import { stubMappings } from './testing';

describe('methodOf', () => {
  it('reads the method the stub matches on, normalized', () => {
    expect(methodOf({ request: { method: 'get' } })).toBe('GET');
    expect(methodOf({ request: { method: ' POST ' } })).toBe('POST');
  });

  it('reports an absent method as ANY, which is what the server reads it as', () => {
    expect(methodOf({})).toBe('ANY');
    expect(methodOf({ request: {} })).toBe('ANY');
    expect(methodOf({ request: { method: '' } })).toBe('ANY');
  });

  it('keeps a method WireMock does not define, because a mock stands in for services that have them', () => {
    expect(methodOf({ request: { method: 'PURGE' } })).toBe('PURGE');
  });
});

describe('urlCriterionOf', () => {
  it('names the field the criterion came from, for each of the five spellings', () => {
    expect(urlCriterionOf({ request: { url: '/a?b=1' } })).toEqual({
      kind: 'url',
      value: '/a?b=1',
    });
    expect(urlCriterionOf({ request: { urlPath: '/a' } })).toEqual({
      kind: 'urlPath',
      value: '/a',
    });
    expect(urlCriterionOf({ request: { urlPattern: '/a.*' } })).toEqual({
      kind: 'urlPattern',
      value: '/a.*',
    });
    expect(urlCriterionOf({ request: { urlPathPattern: '/a/.*' } })).toEqual({
      kind: 'urlPathPattern',
      value: '/a/.*',
    });
    expect(urlCriterionOf({ request: { urlPathTemplate: '/a/{id}' } })).toEqual({
      kind: 'urlPathTemplate',
      value: '/a/{id}',
    });
  });

  it('reports no criterion for a stub that matches every URL', () => {
    expect(urlCriterionOf({})).toBeUndefined();
    expect(urlCriterionOf({ request: { method: 'GET' } })).toBeUndefined();
  });
});

describe('stubIdOf', () => {
  it('accepts either spelling of the identity the server echoes', () => {
    expect(stubIdOf({ id: 'a' })).toBe('a');
    expect(stubIdOf({ uuid: 'b' })).toBe('b');
    expect(stubIdOf({})).toBeUndefined();
  });
});

describe('filterMappings', () => {
  const mappings: StubMapping[] = [
    { request: { method: 'GET', urlPath: '/api/orders' } },
    { request: { method: 'POST', urlPath: '/api/orders' } },
    { request: { method: 'GET', urlPattern: '/api/payments.*' } },
    { request: { method: 'GET' } },
  ];

  it('returns everything when nothing is asked for', () => {
    expect(filterMappings(mappings, { method: '', url: '' })).toHaveLength(4);
  });

  it('narrows by method exactly', () => {
    expect(filterMappings(mappings, { method: 'POST', url: '' })).toHaveLength(1);
  });

  it('narrows by a case-insensitive substring of the URL criterion', () => {
    expect(filterMappings(mappings, { method: '', url: 'ORDERS' })).toHaveLength(2);
    expect(filterMappings(mappings, { method: '', url: 'payments' })).toHaveLength(1);
  });

  it('applies both filters together rather than either', () => {
    expect(filterMappings(mappings, { method: 'GET', url: 'orders' })).toHaveLength(1);
  });

  it('drops a match-any-URL stub from a URL search, so the search stays useful', () => {
    const anyUrl = filterMappings(mappings, { method: '', url: 'a' });
    expect(anyUrl.every((mapping) => urlCriterionOf(mapping) !== undefined)).toBe(true);
  });
});

describe('methodsPresent', () => {
  it('offers the methods that are there, sorted, without duplicates', () => {
    expect(
      methodsPresent([
        { request: { method: 'POST' } },
        { request: { method: 'GET' } },
        { request: { method: 'get' } },
        {},
      ]),
    ).toEqual(['ANY', 'GET', 'POST']);
  });
});

describe('pageOf', () => {
  it('renders at most one page whatever the set holds', () => {
    expect(pageOf(stubMappings(4321), 0)).toHaveLength(PAGE_SIZE);
  });

  it('walks the set in order', () => {
    const items = [1, 2, 3, 4, 5];
    expect(pageOf(items, 0, 2)).toEqual([1, 2]);
    expect(pageOf(items, 1, 2)).toEqual([3, 4]);
    expect(pageOf(items, 2, 2)).toEqual([5]);
  });

  it('clamps a page number the set has shrunk out from under', () => {
    expect(pageOf([1, 2, 3], 99, 2)).toEqual([3]);
    expect(pageOf([1, 2, 3], -4, 2)).toEqual([1, 2]);
  });

  it('answers an empty page for an empty set rather than throwing', () => {
    expect(pageOf([], 0, 2)).toEqual([]);
  });
});

describe('pageCount', () => {
  it('counts partial pages, and never reports zero', () => {
    expect(pageCount(0, 10)).toBe(1);
    expect(pageCount(10, 10)).toBe(1);
    expect(pageCount(11, 10)).toBe(2);
  });
});

describe('pageRange', () => {
  it('reads as a 1-based inclusive range', () => {
    expect(pageRange(11, 0, 10)).toEqual({ first: 1, last: 10 });
    expect(pageRange(11, 1, 10)).toEqual({ first: 11, last: 11 });
  });

  it('reads 0–0 when there is nothing to show', () => {
    expect(pageRange(0, 0, 10)).toEqual({ first: 0, last: 0 });
  });
});
