// SPDX-License-Identifier: Apache-2.0

/**
 * Path arithmetic for the client router. Deliberately free of `window` and of
 * Svelte reactivity: the app is served from a sub-path
 * (`/__admin/mockulus/ui/`), which makes translating between browser locations
 * and route paths the part most likely to break, and the part worth testing
 * directly. The reactive shell lives in `router.svelte.ts`.
 */

/**
 * Collapses repeated slashes and drops a trailing one, so that `/about`,
 * `/about/` and `//about` are the same route. The root is the one path allowed
 * to end in a slash, because it is nothing but one.
 */
export function normalizePath(path: string): string {
  const collapsed = path.replace(/\/{2,}/g, '/');
  const trimmed = collapsed.length > 1 ? collapsed.replace(/\/+$/, '') : collapsed;
  return trimmed === '' ? '/' : trimmed;
}

/**
 * Browser location → route path. `/__admin/mockulus/ui/about` becomes
 * `/about`, and the base itself becomes `/`.
 *
 * A location outside the base is returned unchanged rather than coerced: it
 * means the server mounted us somewhere the build did not expect, and the
 * honest outcome is a route that matches nothing.
 */
export function toRoutePath(pathname: string, base: string = import.meta.env.BASE_URL): string {
  const normalizedBase = normalizePath(base);
  const normalized = normalizePath(pathname);

  if (normalizedBase === '/') {
    return normalized;
  }
  if (normalized === normalizedBase) {
    return '/';
  }
  if (normalized.startsWith(`${normalizedBase}/`)) {
    return normalizePath(normalized.slice(normalizedBase.length));
  }
  return normalized;
}

/**
 * Route path → browser location, the inverse of {@link toRoutePath}. Anchors
 * need the real URL in their `href` so that middle-click and "open in new tab"
 * keep working; only left-clicks are intercepted.
 */
export function toHref(path: string, base: string = import.meta.env.BASE_URL): string {
  const normalizedBase = normalizePath(base);
  const normalized = normalizePath(path);

  if (normalizedBase === '/') {
    return normalized;
  }
  return normalized === '/' ? `${normalizedBase}/` : `${normalizedBase}${normalized}`;
}

/**
 * Finds the route owning a route path, or `undefined` when nothing does — the
 * caller decides what a miss looks like.
 */
export function matchRoute<T extends { readonly path: string }>(
  routes: readonly T[],
  routePath: string,
): T | undefined {
  const normalized = normalizePath(routePath);
  return routes.find((route) => normalizePath(route.path) === normalized);
}
