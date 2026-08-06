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
   * The main region, which is both the skip link's destination and where focus
   * goes after a client-side navigation.
   */
  let main = $state<HTMLElement | null>(null);

  /**
   * What the live region below says. Empty until the first navigation, so a
   * fresh page load announces nothing — the browser has already said what it
   * loaded, and repeating it would be the app talking over the platform.
   */
  let announcement = $state('');

  /** The path the announcement and the focus move were last made for. */
  let announcedFor = router.path;

  /**
   * Navigation, for a reader who is not watching the page.
   *
   * A client-side route change is invisible in two ways that a full page load is
   * not, and both need answering. **Focus** is the first: the control that
   * caused the move is often gone with the view that held it — the journal's
   * "Debug this request" button navigates to the near-miss debugger and unmounts
   * itself — and a focused element that leaves the document drops focus to
   * `<body>`, so the reader's next Tab starts again from the top of the page.
   * Moving focus into the main region puts them at the start of what actually
   * changed. **The announcement** is the second: `document.title` is updated
   * above, and screen readers do not reliably announce a title that changes
   * without a page load, so the new page's name is put into a live region as
   * well. The two are not redundant — the live region says which page, the focus
   * move says where the reader now is.
   *
   * `untrack` on the element is what keeps this an effect about the path: `main`
   * is state so that binding it re-runs nothing, and reading it as a dependency
   * would make the first mount look like a navigation.
   */
  $effect(() => {
    const path = router.path;
    if (path === announcedFor) {
      return;
    }
    announcedFor = path;
    announcement = router.match?.route.title ?? 'Not found';
    untrack(() => main)?.focus();
  });

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

  <!-- `tabindex="-1"` is what makes both the skip link and the route change
       above able to put focus here. Without it the element is not a focus
       target: following the skip link moves the browser's reading position in
       Chrome and moves nothing at all in Safari, so the keyboard user who used
       it is still in the header. `outline-none` because focus arriving here is
       a consequence of an action taken elsewhere rather than a control the
       reader is standing on, and a ring around the whole page would read as one. -->
  <main
    bind:this={main}
    id="main"
    tabindex="-1"
    class="mx-auto w-full max-w-5xl flex-1 px-6 py-10 outline-none"
  >
    <View />
  </main>

  <!-- Off screen and always present. A live region has to be in the document
       before the text arrives, or the assistive technology has nothing to be
       watching when it does. -->
  <p aria-live="polite" class="sr-only">{announcement}</p>

  <footer
    class="border-t border-slate-200 bg-white px-6 py-4 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400"
  >
    Served from the binary at <code class="font-mono">{import.meta.env.BASE_URL}</code>
  </footer>
</div>

<TokenSheet {api} />
