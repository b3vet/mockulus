// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { matchRoute, normalizePath, shouldRouteClick, toHref, toRoutePath } from './router';

/** The sub-path the admin mux serves the UI under. */
const base = '/__admin/mockulus/ui/';

const routes = [
  { path: '/', view: 'overview' },
  { path: '/stubs/:id', view: 'stub-detail' },
  { path: '/stubs', view: 'stubs' },
  { path: '/stubs/new', view: 'stub-new' },
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
  const at = (pathname: string) => matchRoute(routes, toRoutePath(pathname, base));

  it('resolves a browser location to the right view', () => {
    expect(at('/__admin/mockulus/ui/')?.route.view).toBe('overview');
    expect(at('/__admin/mockulus/ui')?.route.view).toBe('overview');
    expect(at('/__admin/mockulus/ui/about')?.route.view).toBe('about');
    expect(at('/__admin/mockulus/ui/stubs')?.route.view).toBe('stubs');
  });

  it('returns undefined for an unknown path rather than guessing', () => {
    expect(at('/__admin/mockulus/ui/nope')).toBeUndefined();
  });

  it('binds a :name segment and reports it as a parameter', () => {
    const match = at('/__admin/mockulus/ui/stubs/2ff9b35d-0189-4fc7-9ae4-0b8a76c0b814');

    expect(match?.route.view).toBe('stub-detail');
    expect(match?.params).toEqual({ id: '2ff9b35d-0189-4fc7-9ae4-0b8a76c0b814' });
  });

  it('percent-decodes a bound segment, so a view reads the id the link was built from', () => {
    expect(at('/__admin/mockulus/ui/stubs/a%2Fb')?.params).toEqual({ id: 'a/b' });
  });

  it('prefers a static route over a parametric one whatever order the table is in', () => {
    // `/stubs/:id` is declared above `/stubs/new`, which is the arrangement that
    // would send a reader to a detail view asking the server for a stub called
    // "new" if declaration order decided.
    expect(at('/__admin/mockulus/ui/stubs/new')?.route.view).toBe('stub-new');
    expect(at('/__admin/mockulus/ui/stubs/new')?.params).toEqual({});
  });

  it('does not let one parameter swallow several segments', () => {
    expect(at('/__admin/mockulus/ui/stubs/one/two')).toBeUndefined();
  });
});

describe('shouldRouteClick', () => {
  const click = (init: Partial<MouseEvent> = {}) =>
    ({
      defaultPrevented: false,
      button: 0,
      metaKey: false,
      ctrlKey: false,
      shiftKey: false,
      altKey: false,
      ...init,
    }) as MouseEvent;

  it('routes a plain left click', () => {
    expect(shouldRouteClick(click())).toBe(true);
  });

  it('leaves the browser to handle the gestures that mean "somewhere else"', () => {
    expect(shouldRouteClick(click({ button: 1 }))).toBe(false);
    expect(shouldRouteClick(click({ metaKey: true }))).toBe(false);
    expect(shouldRouteClick(click({ ctrlKey: true }))).toBe(false);
    expect(shouldRouteClick(click({ shiftKey: true }))).toBe(false);
    expect(shouldRouteClick(click({ altKey: true }))).toBe(false);
    expect(shouldRouteClick(click({ defaultPrevented: true }))).toBe(false);
  });
});

describe('normalizePath', () => {
  it('collapses duplicate slashes and trailing slashes', () => {
    expect(normalizePath('//about//')).toBe('/about');
    expect(normalizePath('/')).toBe('/');
    expect(normalizePath('')).toBe('/');
  });
});
