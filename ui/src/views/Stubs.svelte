<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
  import AppLink from '../lib/components/AppLink.svelte';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import ImportPanel from '../lib/components/ImportPanel.svelte';
  import { getApi } from '../lib/api.svelte';
  import { downloadTextFile } from '../lib/download';
  import { EXPORT_MEDIA_TYPE, exportFileName, toExportDocument } from '../lib/mappings-file';
  import { createResource } from '../lib/resource.svelte';
  import { toHref } from '../lib/router';
  import { getRouter } from '../lib/router.svelte';
  import {
    MAX_LOADED_MAPPINGS,
    filterMappings,
    methodOf,
    methodsPresent,
    pageCount,
    pageOf,
    pageRange,
    stubIdOf,
    urlCriterionOf,
  } from '../lib/stubs';

  const api = getApi();
  const router = getRouter();

  /**
   * How the three filters divide between the server and the browser.
   *
   * Metadata is the server's: `find-by-metadata` exists for exactly this and
   * answers with the matching stubs alone, so a run tagged with its own id
   * never has to be found by reading the whole deployment. Method and URL are
   * the browser's, because the admin API has no parameter for either — a filter
   * over the page the server happened to return would answer a question nobody
   * asked, and would answer it differently depending on how far the reader had
   * paged.
   *
   * That is what forces the snapshot into the browser, and why the read is
   * capped: {@link MAX_LOADED_MAPPINGS} bounds the work, the pager bounds the
   * DOM, and the view says so out loud when the cap is reached rather than
   * quietly showing a prefix.
   */
  interface Loaded {
    readonly mappings: readonly StubMapping[];
    readonly truncated: boolean;
  }

  let methodFilter = $state('');
  let urlFilter = $state('');
  let metadataDraft = $state('');
  let metadataApplied = $state('');
  let page = $state(0);

  async function load(client: MockulusClient): Promise<Loaded> {
    const jsonPath = metadataApplied.trim();
    if (jsonPath !== '') {
      const found = await client.mappings.findByMetadata({ matchesJsonPath: jsonPath });
      return { mappings: found.mappings, truncated: false };
    }

    // `paginate` rather than one unbounded `list`: it is the SDK's own answer to
    // walking the snapshot, and it gets the termination condition right — a
    // short page, which the server produces exactly once — where a hand-written
    // loop stops on a `meta.total` that a concurrent write has moved.
    const mappings: StubMapping[] = [];
    for await (const mapping of client.mappings.paginate({ pageSize: 200 })) {
      mappings.push(mapping);
      if (mappings.length >= MAX_LOADED_MAPPINGS) {
        return { mappings, truncated: true };
      }
    }
    return { mappings, truncated: false };
  }

  const resource = createResource(api, load);

  const loaded = $derived(resource.data?.mappings ?? []);
  const truncated = $derived(resource.data?.truncated ?? false);
  const methods = $derived(methodsPresent(loaded));
  const filtered = $derived(filterMappings(loaded, { method: methodFilter, url: urlFilter }));
  const pages = $derived(pageCount(filtered.length));
  // Clamped on read rather than corrected on write: a filter typed while on
  // page seven shrinks the set underneath the number, and that is a stale
  // number rather than a mistake to report.
  const currentPage = $derived(Math.min(Math.max(0, page), pages - 1));
  const visible = $derived(pageOf(filtered, currentPage));
  const range = $derived(pageRange(filtered.length, currentPage));
  const filtersActive = $derived(
    methodFilter !== '' || urlFilter.trim() !== '' || metadataApplied !== '',
  );

  function applyMetadata(event: SubmitEvent) {
    event.preventDefault();
    const next = metadataDraft.trim();
    page = 0;
    if (next === metadataApplied) {
      return;
    }
    metadataApplied = next;
    resource.reload();
  }

  function clearFilters() {
    methodFilter = '';
    urlFilter = '';
    metadataDraft = '';
    page = 0;
    if (metadataApplied !== '') {
      metadataApplied = '';
      resource.reload();
    }
  }

  let importing = $state(false);

  /**
   * Exports what is on screen, not what is in the deployment.
   *
   * The filters are the point: "every stub this run tagged" is the export
   * somebody actually wants, and it is one metadata search away. Exporting the
   * whole snapshot regardless would make the filters above decorative on the one
   * surface where they do the most work. The button says how many will be
   * written so the two can never be confused.
   */
  function exportVisible() {
    downloadTextFile(exportFileName(new Date()), EXPORT_MEDIA_TYPE, toExportDocument(filtered));
  }
</script>

{#snippet row(mapping: StubMapping)}
  {@const criterion = urlCriterionOf(mapping)}
  <span
    class="rounded bg-slate-200 px-1.5 py-0.5 font-mono text-xs font-semibold text-slate-700 dark:bg-slate-700 dark:text-slate-200"
  >
    {methodOf(mapping)}
  </span>
  <span class="font-mono text-sm break-all">
    {criterion ? criterion.value : 'any URL'}
  </span>
  {#if criterion}
    <span class="text-xs text-slate-500 dark:text-slate-400">{criterion.kind}</span>
  {/if}
  <span class="w-full text-xs text-slate-500 dark:text-slate-400">
    {#if mapping.name}<span class="text-slate-700 dark:text-slate-300">{mapping.name}</span> ·{/if}
    priority {mapping.priority ?? 5}
    {#if mapping.scenarioName}· scenario {mapping.scenarioName}{/if}
    {#if mapping.persistent}· persistent{/if}
    {#each Object.keys(mapping.metadata ?? {}) as key (key)}
      <span
        class="ml-1 rounded-full border border-slate-300 px-1.5 py-0.5 font-mono dark:border-slate-700"
        >{key}</span
      >
    {/each}
  </span>
{/snippet}

<div class="flex flex-wrap items-baseline justify-between gap-3">
  <h1 class="text-2xl font-semibold tracking-tight">Stubs</h1>
  <div class="flex flex-wrap items-center gap-2">
    <AppLink
      href={toHref('/stubs/new')}
      onnavigate={(href) => router.navigate(href)}
      class="rounded-md bg-sky-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-800"
    >
      New stub
    </AppLink>
    <button
      type="button"
      aria-expanded={importing}
      onclick={() => (importing = !importing)}
      class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Import…
    </button>
    <button
      type="button"
      disabled={filtered.length === 0}
      onclick={exportVisible}
      class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Export {filtered.length.toLocaleString()}
    </button>
    <button
      type="button"
      onclick={() => resource.reload()}
      class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Refresh
    </button>
  </div>
</div>

<p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
  The mappings this replica compiled into its snapshot — the stubs it will actually match against.
  Export writes the stubs listed below, filters and all, as a file the import endpoint takes back
  unchanged.
</p>

{#if importing}
  <div class="mt-6">
    <ImportPanel onimported={() => resource.reload()} />
  </div>
{/if}

<form aria-label="Filter stubs" onsubmit={applyMetadata} class="mt-6 grid gap-4 sm:grid-cols-3">
  <div>
    <label for="stub-method" class="block text-sm font-medium">Method</label>
    <select
      id="stub-method"
      bind:value={methodFilter}
      onchange={() => (page = 0)}
      class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
    >
      <option value="">Any method</option>
      {#each methods as method (method)}
        <option value={method}>{method}</option>
      {/each}
    </select>
  </div>

  <div>
    <label for="stub-url" class="block text-sm font-medium">URL contains</label>
    <input
      id="stub-url"
      type="search"
      bind:value={urlFilter}
      oninput={() => (page = 0)}
      placeholder="/api/orders"
      class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
    />
  </div>

  <div>
    <label for="stub-metadata" class="block text-sm font-medium">Metadata (JSONPath)</label>
    <div class="mt-1.5 flex gap-2">
      <input
        id="stub-metadata"
        type="search"
        bind:value={metadataDraft}
        aria-describedby="stub-metadata-help"
        placeholder="$.team"
        class="w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
      />
      <button
        type="submit"
        class="rounded-md border border-slate-300 px-3 py-2 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
      >
        Apply
      </button>
    </div>
    <p id="stub-metadata-help" class="mt-1 text-xs text-slate-500 dark:text-slate-400">
      Searched by the server. A path the server cannot compile is refused, and the refusal is shown
      below.
    </p>
  </div>
</form>

<div class="mt-8">
  {#if resource.error}
    <ErrorState
      error={resource.error}
      onretry={() => resource.reload()}
      onauthenticate={() => api.requestToken()}
    />
  {:else if resource.loading && resource.data === undefined}
    <p role="status" class="text-sm text-slate-600 dark:text-slate-400">Loading stubs…</p>
  {:else}
    {#if truncated}
      <p
        class="mb-4 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-200"
      >
        This deployment holds more than {MAX_LOADED_MAPPINGS.toLocaleString()} stubs. Only the first
        {MAX_LOADED_MAPPINGS.toLocaleString()} were read, so the filters below search those and not the
        whole snapshot. Narrow with the metadata search, which the server resolves.
      </p>
    {/if}

    {#if filtered.length === 0}
      <div
        class="rounded-lg border border-slate-300 bg-white px-5 py-8 text-center dark:border-slate-700 dark:bg-slate-900"
      >
        {#if loaded.length === 0 && !filtersActive}
          <h2 class="text-base font-semibold">No stubs registered</h2>
          <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
            This replica's snapshot is empty. Register one with
            <code class="font-mono">POST /__admin/mappings</code>, the admin SDK, or any
            WireMock-compatible client, and it will appear here.
          </p>
        {:else}
          <h2 class="text-base font-semibold">No stub matches these filters</h2>
          <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
            {loaded.length.toLocaleString()} stub{loaded.length === 1 ? '' : 's'} were read; none of them
            satisfies every filter.
          </p>
          <button
            type="button"
            onclick={clearFilters}
            class="mt-4 rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
          >
            Clear filters
          </button>
        {/if}
      </div>
    {:else}
      <ul
        aria-label="Stub mappings"
        class="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900"
      >
        {#each visible as mapping, index (stubIdOf(mapping) ?? index)}
          {@const id = stubIdOf(mapping)}
          <li>
            {#if id}
              <AppLink
                href={toHref(`/stubs/${encodeURIComponent(id)}`)}
                onnavigate={(href) => router.navigate(href)}
                class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 hover:bg-slate-50 focus:outline-2 focus:-outline-offset-2 focus:outline-sky-600 dark:hover:bg-slate-800"
              >
                {@render row(mapping)}
              </AppLink>
            {:else}
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 opacity-70">
                {@render row(mapping)}
                <span class="w-full text-xs text-amber-700 dark:text-amber-400">
                  No id in the document, so this stub has no detail view.
                </span>
              </div>
            {/if}
          </li>
        {/each}
      </ul>

      <nav
        aria-label="Stub list pages"
        class="mt-4 flex flex-wrap items-center justify-between gap-3"
      >
        <p aria-live="polite" class="text-sm text-slate-600 dark:text-slate-400">
          Showing {range.first.toLocaleString()}–{range.last.toLocaleString()} of {filtered.length.toLocaleString()}
          {#if filtered.length !== loaded.length}
            (filtered from {loaded.length.toLocaleString()})
          {/if}
          {#if resource.loading}· refreshing…{/if}
        </p>
        <div class="flex items-center gap-2">
          <button
            type="button"
            aria-label="Previous page"
            disabled={currentPage === 0}
            onclick={() => (page = currentPage - 1)}
            class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:hover:bg-slate-800"
          >
            Previous
          </button>
          <span class="text-sm text-slate-600 dark:text-slate-400">
            Page {currentPage + 1} of {pages}
          </span>
          <button
            type="button"
            aria-label="Next page"
            disabled={currentPage >= pages - 1}
            onclick={() => (page = currentPage + 1)}
            class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:hover:bg-slate-800"
          >
            Next
          </button>
        </div>
      </nav>
    {/if}
  {/if}
</div>
