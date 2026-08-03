<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { isMockulusError, type MockulusClient, type StubMapping } from '@mockulus/admin-sdk';
  import OpsDangerZone from '../lib/components/OpsDangerZone.svelte';
  import OpsFiles from '../lib/components/OpsFiles.svelte';
  import OpsOverview from '../lib/components/OpsOverview.svelte';
  import OpsSettings from '../lib/components/OpsSettings.svelte';
  import { getApi } from '../lib/api.svelte';
  import { createResource } from '../lib/resource.svelte';
  import { MAX_LOADED_MAPPINGS } from '../lib/stubs';

  const api = getApi();

  /**
   * The deployment as an operator sees it, and the four things they can do to
   * it.
   *
   * Every read the page makes is owned here rather than inside the panel that
   * renders it, for one reason: the panels fail together in a way that is worth
   * explaining once. During a store outage the settings and the file listing
   * both answer 503 with code 1020 while health and the mappings keep answering
   * from the compiled snapshot, and three separate error boxes describing the
   * same outage would leave the reader to work out that it is one. Owning the
   * resources here is what lets the page say it.
   */
  const health = createResource(api, (client) => client.system.health());
  const version = createResource(api, (client) => client.system.version());
  const settings = createResource(api, (client) => client.settings.get());
  const files = createResource(api, (client) => client.files.list());

  /**
   * The mappings, read for the files panel's `bodyFileName` back-links.
   *
   * Capped at the same wall the stub list uses, and for the same reason: the
   * question is answered in the browser, so the answer costs a read whose size
   * the deployment decides. Reaching the cap makes a back-link possibly
   * incomplete rather than wrong, and the panel says so where it matters.
   */
  const mappings = createResource(
    api,
    async (client: MockulusClient): Promise<readonly StubMapping[]> => {
      const collected: StubMapping[] = [];
      for await (const mapping of client.mappings.paginate({ pageSize: 200 })) {
        collected.push(mapping);
        if (collected.length >= MAX_LOADED_MAPPINGS) {
          break;
        }
      }
      return collected;
    },
  );

  /**
   * Whether the store is down, judged from the two reads that touch it.
   *
   * Health and the mappings listing are deliberately not consulted: both read
   * this replica's compiled snapshot and keep answering through an outage, so
   * asking them would never detect one. That asymmetry is the degraded mode
   * working as designed rather than a gap, and it is exactly what the banner
   * below exists to say.
   */
  const storeDown = $derived(
    [settings.error, files.error].some((err) => isMockulusError(err) && err.isStoreUnavailable),
  );

  function reloadAll() {
    health.reload();
    settings.reload();
    files.reload();
    mappings.reload();
  }
</script>

<div class="flex flex-wrap items-baseline justify-between gap-3">
  <h1 class="text-2xl font-semibold tracking-tight">Ops</h1>
  <button
    type="button"
    onclick={reloadAll}
    class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
  >
    Refresh
  </button>
</div>

{#if storeDown}
  <section
    role="status"
    aria-labelledby="ops-store-outage-heading"
    class="mt-6 rounded-lg border border-amber-300 bg-amber-50 px-5 py-4 text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-100"
  >
    <h2 id="ops-store-outage-heading" class="text-base font-semibold">
      The stub store is unavailable — this deployment is degraded, not down
    </h2>
    <p class="mt-2 max-w-2xl text-sm">
      Mock traffic is still being served. Every replica matches against the snapshot it has already
      compiled, so the stubs registered before the outage keep answering exactly as they did, and
      the overview below is still this replica's own truth: it reads the snapshot and never the
      store.
    </p>
    <div class="mt-3 grid max-w-3xl gap-4 text-sm sm:grid-cols-2">
      <div>
        <p class="text-xs font-semibold tracking-wide uppercase">Still working</p>
        <ul class="mt-1 space-y-1">
          <li>Serving mock requests from every replica's snapshot.</li>
          <li>Health, version, and the stub listing — all snapshot reads.</li>
          <li>The scenario listing's definitions, which come from the snapshot.</li>
        </ul>
      </div>
      <div>
        <p class="text-xs font-semibold tracking-wide uppercase">Refused until the store is back</p>
        <ul class="mt-1 space-y-1">
          <li>Every admin write: registering, editing or deleting a stub.</li>
          <li>Uploading or deleting a response-body file, and saving settings.</li>
          <li>Setting a scenario state, and every call in the danger zone.</li>
        </ul>
      </div>
    </div>
    <p class="mt-3 max-w-2xl text-sm">
      Nothing here needs undoing. An admin write refused with code 1020 did not half-happen — it was
      refused before anything was persisted — so the fix is to restore the store this deployment
      resolved to, named under Store below, and try again.
    </p>
  </section>
{/if}

<p class="mt-4 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
  Health and version come from this replica; files, settings and the resets below are
  deployment-wide and reach every replica through the store.
</p>

<div class="mt-8 space-y-12">
  <OpsOverview {health} {version} />
  <OpsFiles {files} {mappings} />
  <OpsSettings {settings} />
  <OpsDangerZone ondone={reloadAll} />
</div>
