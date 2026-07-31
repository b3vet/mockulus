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

  const planned = [
    'Stub editor — create, edit, duplicate and delete, with the server’s 422s inline',
    'Journal — the request log, matched and unmatched',
    'Near-miss debugger — why a request did not match',
    'Scenarios — current state and one-click transitions',
    'Ops — health, files, settings, danger zone',
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
  Coming in later stages
</h2>
<ul class="mt-3 space-y-2">
  {#each planned as item (item)}
    <li class="flex gap-3 text-slate-700 dark:text-slate-300">
      <span aria-hidden="true" class="text-slate-400 dark:text-slate-600">—</span>
      <span>{item}</span>
    </li>
  {/each}
</ul>
