// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { matchRoute, normalizePath, toHref, toRoutePath } from './router';

/** The sub-path the admin mux serves the UI under. */
const base = '/__admin/mockulus/ui/';

const routes = [
  { path: '/', view: 'overview' },
  { path: '/about', view: 'about' },
] as const;

describe('toRoutePath', () => {
  it('maps the base itself to the root route, with or without a trailing slash', () => {
    expect(toRoutePath('/__admin/mockulus/ui/', base)).toBe('/');
    expect(toRoutePath('/__admin/mockulus/ui', base)).toBe('/');
  });

  it('strips the base from a nested location', () => {
    expect(toRoutePath('/__admin/mockulus/ui/about', base)).toBe('/about');
    expect(toRoutePath('/__admin/mockulus/ui/about/', base)).toBe('/about');
  });

  it('leaves a location outside the base alone, so it matches nothing', () => {
    expect(toRoutePath('/__admin/mappings', base)).toBe('/__admin/mappings');
  });

  it('does not mistake a sibling path that merely shares the prefix', () => {
    expect(toRoutePath('/__admin/mockulus/uixyz', base)).toBe('/__admin/mockulus/uixyz');
  });
});

describe('toHref', () => {
  it('is the inverse of toRoutePath', () => {
    for (const path of ['/', '/about']) {
      expect(toRoutePath(toHref(path, base), base)).toBe(path);
    }
  });

  it('produces real, base-prefixed URLs for anchors', () => {
    expect(toHref('/', base)).toBe('/__admin/mockulus/ui/');
    expect(toHref('/about', base)).toBe('/__admin/mockulus/ui/about');
  });
});

describe('matchRoute', () => {
  it('resolves a browser location to the right view', () => {
    const at = (pathname: string) => matchRoute(routes, toRoutePath(pathname, base))?.view;

    expect(at('/__admin/mockulus/ui/')).toBe('overview');
    expect(at('/__admin/mockulus/ui')).toBe('overview');
    expect(at('/__admin/mockulus/ui/about')).toBe('about');
  });

  it('returns undefined for an unknown path rather than guessing', () => {
    expect(matchRoute(routes, toRoutePath('/__admin/mockulus/ui/nope', base))).toBeUndefined();
  });
});

describe('normalizePath', () => {
  it('collapses duplicate slashes and trailing slashes', () => {
    expect(normalizePath('//about//')).toBe('/about');
    expect(normalizePath('/')).toBe('/');
    expect(normalizePath('')).toBe('/');
  });
});
