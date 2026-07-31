<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { untrack } from 'svelte';
  import { createApi, setApi, type Api } from './lib/api.svelte';
  import NavLink from './lib/components/NavLink.svelte';
  import TokenSheet from './lib/components/TokenSheet.svelte';
  import { normalizePath, toHref } from './lib/router';
  import { createRouter, setRouter } from './lib/router.svelte';
  import { navRoutes, routes, type RouteDefinition } from './lib/routes';
  import NotFound from './views/NotFound.svelte';

  interface Props {
    /**
     * The API layer, for a test that would rather not have one built around the
     * real `fetch`. The app supplies its own, which is the only configuration
     * production ever uses.
     */
    api?: Api;
  }

  let { api = createApi() }: Props = $props();

  const router = createRouter<RouteDefinition>(routes);

  // Both go in context here, once, at the top. Everything below reaches them
  // through `getApi()` and `getRouter()` rather than through props, because the
  // views are mounted generically — the shell renders whatever component the
  // route table names and cannot hand each one a different set.
  //
  // `untrack` says out loud what context already implies: the api is captured
  // once, for the life of the app. Its contents are reactive — `hasToken` and
  // `tokenRequested` are getters over state — so nothing here needs the binding
  // itself to be, and Svelte is right to want that stated rather than assumed.
  setApi(untrack(() => api));
  setRouter(router);

  const View = $derived(router.match?.route.component ?? NotFound);

  /**
   * A section is current for the whole subtree under it, so the Stubs tab stays
   * marked while a stub's detail page is open. Without this the nav would go
   * blank one click into the only section that has a detail view.
   */
  function isCurrentSection(path: string): boolean {
    const route = normalizePath(path);
    const here = normalizePath(router.path);
    return here === route || (route !== '/' && here.startsWith(`${route}/`));
  }

  $effect(() => {
    const title = router.match?.route.title;
    document.title = title === undefined ? 'Not found · mockulus' : `${title} · mockulus`;
  });
</script>

<div
  class="flex min-h-screen flex-col bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100"
>
  <!-- The nav is short, but the stub list below it is not, and a keyboard user
       arriving on page seven should not tab through the header to reach it. -->
  <a
    href="#main"
    class="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:m-2 focus:rounded-md focus:bg-white focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:shadow dark:focus:bg-slate-900"
  >
    Skip to content
  </a>

  <header class="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <div class="mx-auto flex max-w-5xl flex-wrap items-center gap-x-6 gap-y-2 px-6 py-4">
      <span class="text-lg font-semibold tracking-tight">mockulus</span>
      <span
        class="rounded-full border border-slate-300 px-2 py-0.5 text-xs text-slate-500 dark:border-slate-700 dark:text-slate-400"
      >
        admin
      </span>
      <nav aria-label="Primary" class="flex gap-1">
        {#each navRoutes as route (route.path)}
          <NavLink
            href={toHref(route.path)}
            label={route.label ?? route.path}
            active={isCurrentSection(route.path)}
            onnavigate={(href) => router.navigate(href)}
          />
        {/each}
      </nav>

      <!-- Shown only once a token has been taken. A deployment that never asked
           for one has nothing to say here, and a control offering to set a token
           nobody needs would invite the reader to look for a problem. -->
      {#if api.hasToken}
        <div class="ms-auto flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          <span>Admin token set for this tab</span>
          <button
            type="button"
            onclick={() => api.clearToken()}
            class="rounded-md border border-slate-300 px-2 py-1 font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
          >
            Forget it
          </button>
        </div>
      {/if}
    </div>
  </header>

  <main id="main" class="mx-auto w-full max-w-5xl flex-1 px-6 py-10">
    <View />
  </main>

  <footer
    class="border-t border-slate-200 bg-white px-6 py-4 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400"
  >
    Served from the binary at <code class="font-mono">{import.meta.env.BASE_URL}</code>
  </footer>
</div>

<TokenSheet {api} />
