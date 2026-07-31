// SPDX-License-Identifier: Apache-2.0
import { getContext, setContext } from 'svelte';
import { matchRoute, toRoutePath, type RouteMatch } from './router';

export interface Router<T extends { readonly path: string }> {
  /** The current route path, base-stripped and normalized. */
  readonly path: string;
  /** The route claiming that path, and what its `:name` segments bound. */
  readonly match: RouteMatch<T> | undefined;
  /** The matched route's path parameters; empty when nothing matched. */
  readonly params: Readonly<Record<string, string>>;
  /** Navigates to a browser location produced by `toHref`. */
  navigate(href: string): void;
  /** Detaches the popstate listener. */
  destroy(): void;
}

/**
 * History-mode routing over `window.history`, which the server's SPA fallback
 * makes safe: any unknown path under the UI prefix serves `index.html`, so a
 * deep link survives a reload.
 *
 * The route table is passed in rather than imported so that this module stays
 * ignorant of the views. `routes.ts` imports every view, so a router that
 * imported it back would put the whole app in the import graph of the one piece
 * of it that has to be testable on its own.
 */
export function createRouter<T extends { readonly path: string }>(
  routes: readonly T[],
  base: string = import.meta.env.BASE_URL,
): Router<T> {
  let pathname = $state(window.location.pathname);

  const sync = () => {
    pathname = window.location.pathname;
  };
  window.addEventListener('popstate', sync);

  const match = $derived(matchRoute(routes, toRoutePath(pathname, base)));

  return {
    get path() {
      return toRoutePath(pathname, base);
    },
    get match() {
      return match;
    },
    get params() {
      return match?.params ?? {};
    },
    navigate(href: string) {
      if (href === window.location.pathname) {
        return;
      }
      window.history.pushState({}, '', href);
      pathname = href;
    },
    destroy() {
      window.removeEventListener('popstate', sync);
    },
  };
}

/**
 * The router goes in context rather than through props because the views are
 * mounted generically: the shell renders whatever component the route table
 * names, so it cannot hand each one a different set of props, and the detail
 * view still has to read the id its own path bound.
 */
const ROUTER_KEY = Symbol('mockulus.router');

export function setRouter<T extends { readonly path: string }>(router: Router<T>): void {
  setContext(ROUTER_KEY, router);
}

// The default type argument is what lets a view call `getRouter()` for nothing
// but `params` and `navigate`. Naming the route type would mean importing
// `routes.ts`, which imports every view, so each view would sit in its own
// import cycle for a type it does not use.
export function getRouter<
  T extends { readonly path: string } = { readonly path: string },
>(): Router<T> {
  const router = getContext<Router<T> | undefined>(ROUTER_KEY);
  if (!router) {
    throw new Error('mockulus ui: no router in context; mount this view inside App');
  }
  return router;
}
