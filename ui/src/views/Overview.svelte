<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import AppLink from '../lib/components/AppLink.svelte';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import { getApi } from '../lib/api.svelte';
  import { createResource } from '../lib/resource.svelte';
  import { toHref } from '../lib/router';
  import { getRouter } from '../lib/router.svelte';

  const api = getApi();
  const router = getRouter();

  /**
   * One call, and it is `version` on purpose.
   *
   * It is the cheapest question the admin API answers, it says what is on the
   * other end rather than merely that something is, and — being the first call
   * the app makes — it is where a token-protected deployment announces itself.
   * The 401 arrives here, so the token sheet opens over this page rather than
   * over a half-drawn list.
   *
   * The health, store and epoch panel belongs to the ops area, and is not built
   * a second time here to fill the page in the meantime.
   */
  const version = createResource(api, (client) => client.system.version());

  /**
   * The areas, as links rather than as a list of promises.
   *
   * This was a "coming in later stages" list while they were being built, and
   * it outlived them: every one landed and the front page went on telling a
   * first-time reader the product was unfinished. A list of what exists is only
   * worth the space if it takes you there, so these are links.
   */
  const areas = [
    {
      path: '/stubs',
      name: 'Stubs',
      what: 'browse, filter, and edit — the server’s 422s land on the field they name',
    },
    {
      path: '/journal',
      name: 'Journal',
      what: 'the request log, matched and unmatched; off by default',
    },
    {
      path: '/near-misses',
      name: 'Near misses',
      what: 'why a request did not match, with or without the journal',
    },
    { path: '/scenarios', name: 'Scenarios', what: 'current state, and one click to move it' },
    { path: '/ops', name: 'Ops', what: 'health, files, settings, and the destructive actions' },
  ];
</script>

<h1 class="text-2xl font-semibold tracking-tight">Overview</h1>

<p class="mt-3 max-w-2xl text-slate-600 dark:text-slate-400">
  This UI ships inside the mockulus binary and talks to the same public admin API every other client
  does, through the typed admin SDK. It has no private endpoint and keeps no server-side session.
</p>

<div class="mt-8">
  {#if version.error}
    <ErrorState
      error={version.error}
      onretry={() => version.reload()}
      onauthenticate={() => api.requestToken()}
    />
  {:else if version.loading && version.data === undefined}
    <p role="status" class="text-sm text-slate-600 dark:text-slate-400">
      Connecting to the admin API…
    </p>
  {:else if version.data}
    <dl
      class="grid gap-x-8 gap-y-3 rounded-lg border border-slate-200 bg-white px-5 py-4 text-sm sm:grid-cols-[max-content_1fr] dark:border-slate-800 dark:bg-slate-900"
    >
      <dt class="font-semibold text-slate-500 dark:text-slate-400">Server</dt>
      <dd class="font-mono">{version.data.version}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">WireMock surface</dt>
      <dd class="font-mono">{version.data.guessedWireMockVersion}</dd>

      {#if version.data.goVersion}
        <dt class="font-semibold text-slate-500 dark:text-slate-400">Built with</dt>
        <dd class="font-mono">{version.data.goVersion}</dd>
      {/if}
    </dl>

    <p class="mt-4 text-sm">
      <AppLink
        href={toHref('/stubs')}
        onnavigate={(href) => router.navigate(href)}
        class="font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
      >
        Browse the stubs this replica is serving →
      </AppLink>
    </p>
  {/if}
</div>

<h2 class="mt-10 text-sm font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400">
  What is here
</h2>
<ul class="mt-3 space-y-2">
  {#each areas as item (item.path)}
    <li class="flex gap-3 text-slate-700 dark:text-slate-300">
      <!-- A visible glyph is held to the contrast floor whether or not a screen
           reader is told to skip it: `aria-hidden` decides who hears it, and a
           low-vision reader is looking at it either way. slate-400 on the light
           page and slate-600 on the dark one were both under 3:1. -->
      <span aria-hidden="true" class="text-slate-500 dark:text-slate-400">—</span>
      <span><AppLink href={toHref(item.path)}>{item.name}</AppLink> — {item.what}</span>
    </li>
  {/each}
</ul>
