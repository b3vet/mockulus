<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import NavLink from './lib/components/NavLink.svelte';
  import { matchRoute, toHref } from './lib/router';
  import { createRouter } from './lib/router.svelte';
  import { routes } from './lib/routes';
  import NotFound from './views/NotFound.svelte';

  const router = createRouter();

  const match = $derived(matchRoute(routes, router.path));
  const View = $derived(match?.component ?? NotFound);

  $effect(() => {
    document.title = match ? `${match.title} · mockulus` : 'Not found · mockulus';
  });
</script>

<div
  class="flex min-h-screen flex-col bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100"
>
  <header class="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <div class="mx-auto flex max-w-4xl flex-wrap items-center gap-x-6 gap-y-2 px-6 py-4">
      <span class="text-lg font-semibold tracking-tight">mockulus</span>
      <span
        class="rounded-full border border-slate-300 px-2 py-0.5 text-xs text-slate-500 dark:border-slate-700 dark:text-slate-400"
      >
        admin
      </span>
      <nav aria-label="Primary" class="flex gap-1">
        {#each routes as route (route.path)}
          <NavLink
            href={toHref(route.path)}
            label={route.label}
            active={route.path === router.path}
            onnavigate={(href) => router.navigate(href)}
          />
        {/each}
      </nav>
    </div>
  </header>

  <main class="mx-auto w-full max-w-4xl flex-1 px-6 py-10">
    <View />
  </main>

  <footer
    class="border-t border-slate-200 bg-white px-6 py-4 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400"
  >
    Served from the binary at <code class="font-mono">{import.meta.env.BASE_URL}</code>
  </footer>
</div>
