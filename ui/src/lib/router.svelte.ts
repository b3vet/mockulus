// SPDX-License-Identifier: Apache-2.0
import { toRoutePath } from './router';

export interface Router {
  /** The current route path, base-stripped and normalized. */
  readonly path: string;
  /** Navigates to a browser location produced by `toHref`. */
  navigate(href: string): void;
  /** Detaches the popstate listener. */
  destroy(): void;
}

/**
 * History-mode routing over `window.history`, which the server's SPA fallback
 * makes safe: any unknown path under the UI prefix serves `index.html`, so a
 * deep link survives a reload.
 */
export function createRouter(base: string = import.meta.env.BASE_URL): Router {
  let pathname = $state(window.location.pathname);

  const sync = () => {
    pathname = window.location.pathname;
  };
  window.addEventListener('popstate', sync);

  return {
    get path() {
      return toRoutePath(pathname, base);
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
