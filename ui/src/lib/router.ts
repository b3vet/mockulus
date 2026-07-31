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

/** A route pattern that matched, together with what its `:name` segments bound. */
export interface RouteMatch<T> {
  readonly route: T;
  /** The bound path parameters, percent-decoded. Empty for a static route. */
  readonly params: Readonly<Record<string, string>>;
}

/**
 * Splits a normalized path into its segments. The root has none, which is what
 * lets `/` and `/stubs` be compared by length with no special case.
 */
function segmentsOf(path: string): string[] {
  const normalized = normalizePath(path);
  return normalized === '/' ? [] : normalized.slice(1).split('/');
}

/**
 * Finds the route owning a route path, or `undefined` when nothing does — the
 * caller decides what a miss looks like.
 *
 * A pattern segment written `:name` matches any single segment and binds it.
 * That is the whole of the pattern language: the stub detail view needs an id
 * in the path, and a route table of this size does not need wildcards, optional
 * segments or a ranking algorithm to go with them.
 *
 * A static match wins over a parametric one whatever order the table is written
 * in, so a `/stubs/new` added later cannot be shadowed by a `/stubs/:id` above
 * it — a failure that shows up as the detail view asking the server for a stub
 * whose id is "new".
 */
export function matchRoute<T extends { readonly path: string }>(
  routes: readonly T[],
  routePath: string,
): RouteMatch<T> | undefined {
  const actual = segmentsOf(routePath);
  let parametric: RouteMatch<T> | undefined;

  for (const route of routes) {
    const pattern = segmentsOf(route.path);
    if (pattern.length !== actual.length) {
      continue;
    }

    const params: Record<string, string> = {};
    let matched = true;
    for (const [index, expected] of pattern.entries()) {
      const segment = actual[index];
      if (segment === undefined) {
        matched = false;
        break;
      }
      if (expected.startsWith(':')) {
        // The location comes from the address bar, so the segment is still
        // percent-encoded. Decoding here means a view reads back the id the
        // link was built from rather than its wire spelling.
        params[expected.slice(1)] = decodeURIComponent(segment);
        continue;
      }
      if (expected !== segment) {
        matched = false;
        break;
      }
    }
    if (!matched) {
      continue;
    }

    const match: RouteMatch<T> = { route, params };
    if (Object.keys(params).length === 0) {
      return match;
    }
    parametric ??= match;
  }

  return parametric;
}

/**
 * Whether a click on an anchor should be routed in-app.
 *
 * Anything that is not a plain left-click — a modifier, the middle button — is
 * the user asking the browser for a new tab or window, and taking that over
 * would break the affordance the anchor's real `href` exists to preserve.
 */
export function shouldRouteClick(event: MouseEvent): boolean {
  return (
    !event.defaultPrevented &&
    event.button === 0 &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.shiftKey &&
    !event.altKey
  );
}
