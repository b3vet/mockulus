<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { Health, VersionInfo } from '@mockulus/admin-sdk';
  import ErrorState from './ErrorState.svelte';
  import { formatTimestamp, formatUptime } from '../ops-overview';
  import type { Resource } from '../resource.svelte';

  /**
   * What this replica is and what it is holding.
   *
   * Two calls rather than one because they answer different questions and fail
   * differently. `health` is per-replica and reads the compiled snapshot — the
   * stub count and the epoch are this pod's, not the deployment's, and two
   * replicas reporting different epochs are mid-propagation rather than
   * inconsistent (deviation #11). `version` is the surface claim, and it is the
   * field that says what is actually on the other end of the connection; a
   * probe that only established reachability has established nothing, which is
   * a mistake this repository has paid for once already.
   *
   * Neither call touches the store, which is why this panel keeps answering
   * during a store outage while the two below it do not.
   */
  interface Props {
    health: Resource<Health>;
    version: Resource<VersionInfo>;
  }

  let { health, version }: Props = $props();

  const document = $derived(health.data);
</script>

<section aria-labelledby="ops-overview-heading">
  <h2 id="ops-overview-heading" class="text-lg font-semibold tracking-tight">Overview</h2>

  {#if health.error}
    <div class="mt-3">
      <ErrorState error={health.error} onretry={() => health.reload()} />
    </div>
  {:else if document === undefined}
    <p role="status" class="mt-3 text-sm text-slate-600 dark:text-slate-400">Reading health…</p>
  {:else}
    <dl
      class="mt-3 grid gap-x-8 gap-y-3 rounded-lg border border-slate-200 bg-white px-5 py-4 text-sm sm:grid-cols-[max-content_1fr] dark:border-slate-800 dark:bg-slate-900"
    >
      <dt class="font-semibold text-slate-500 dark:text-slate-400">Status</dt>
      <dd>
        {document.status} · {document.message}
      </dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Store</dt>
      <dd class="font-mono">{document.store.driver}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Stubs in the snapshot</dt>
      <dd>{document.stubs.toLocaleString()}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Snapshot epoch</dt>
      <dd class="font-mono">{document.epoch}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Uptime</dt>
      <dd>{formatUptime(document.uptimeInSeconds)}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Server time</dt>
      <dd>{formatTimestamp(document.timestamp)}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Version</dt>
      <dd class="font-mono">{document.version}</dd>

      {#if version.data}
        <dt class="font-semibold text-slate-500 dark:text-slate-400">WireMock surface</dt>
        <dd class="font-mono">{version.data.guessedWireMockVersion}</dd>

        {#if version.data.goVersion}
          <dt class="font-semibold text-slate-500 dark:text-slate-400">Built with</dt>
          <dd class="font-mono">{version.data.goVersion}</dd>
        {/if}
      {/if}
    </dl>

    <p class="mt-3 max-w-2xl text-xs text-slate-500 dark:text-slate-400">
      The stub count and the epoch are <em>this replica's</em> compiled snapshot, not the
      deployment's store. Two replicas answering with different epochs are mid-propagation rather
      than inconsistent. Status reads
      <code class="font-mono">healthy</code> whenever the handler answers at all — a replica that
      cannot answer is not reporting a degraded status, it is not answering, which is what
      <code class="font-mono">/healthz</code>
      and <code class="font-mono">/readyz</code> are for.
    </p>
    <p class="mt-2 max-w-2xl text-xs text-slate-500 dark:text-slate-400">
      Stubs excluded from the snapshot because they would not compile are counted by
      <code class="font-mono">mockulus_snapshot_quarantined_total</code>
      on the <code class="font-mono">/metrics</code> endpoint. That endpoint is outside
      <code class="font-mono">/__admin</code>, so it is outside the admin SDK this UI talks through
      and the number cannot be shown here.
    </p>

    {#if version.error}
      <div class="mt-3">
        <ErrorState error={version.error} onretry={() => version.reload()} />
      </div>
    {/if}
  {/if}
</section>
