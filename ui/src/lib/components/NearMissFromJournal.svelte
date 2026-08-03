<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { isMockulusError, type MockulusClient, type NearMissList } from '@mockulus/admin-sdk';
  import ErrorState from './ErrorState.svelte';
  import NearMissCandidates from './NearMissCandidates.svelte';
  import { getApi } from '../api.svelte';
  import { groupByRequest } from '../near-miss-model';
  import { createResource } from '../resource.svelte';

  /**
   * The debugger's journal mode: every recorded request that matched nothing,
   * scored against the stubs this replica holds now.
   *
   * It is a component rather than a branch of the view so that the read is spent
   * only once somebody opens the tab. The journal is off by default, so on an
   * unconfigured deployment this call is a guaranteed 1010, and the mode beside
   * it — which needs no journal — is the one most readers arriving here want.
   */
  interface Props {
    /** Moves the reader to the mode that works without a journal. */
    oncompose: () => void;
  }

  let { oncompose }: Props = $props();

  const api = getApi();

  const resource = createResource(api, async (client: MockulusClient): Promise<NearMissList> =>
    client.requests.unmatchedNearMisses(),
  );

  const groups = $derived(groupByRequest(resource.data?.nearMisses ?? []));
  const journalOff = $derived(isMockulusError(resource.error) && resource.error.isJournalDisabled);
</script>

<div class="mt-4 flex flex-wrap items-center justify-between gap-3">
  <p class="max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    Every recorded request nothing served, with the stubs that came closest. Scored on demand
    against this replica's snapshot, so a stub registered a moment ago is taken into account.
  </p>
  {#if !journalOff}
    <button
      type="button"
      onclick={() => resource.reload()}
      class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Re-score
    </button>
  {/if}
</div>

{#if journalOff}
  <div class="mt-4">
    <!-- No retry offered: 1010 is what this deployment answers until its
         configuration changes, and this mode is the one that needs the journal. -->
    <ErrorState error={resource.error} />
  </div>
  <p class="mt-3 text-sm text-slate-600 dark:text-slate-400">
    This mode reads the journal, so it needs one. The other mode does not.
  </p>
  <button
    type="button"
    onclick={oncompose}
    class="mt-2 rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
  >
    Describe a request instead
  </button>
{:else if resource.error}
  <div class="mt-4">
    <ErrorState
      error={resource.error}
      onretry={() => resource.reload()}
      onauthenticate={() => api.requestToken()}
    />
  </div>
{:else if resource.loading && resource.data === undefined}
  <p role="status" class="mt-4 text-sm text-slate-600 dark:text-slate-400">
    Scoring the unmatched requests…
  </p>
{:else if groups.length === 0}
  <div
    class="mt-4 rounded-lg border border-slate-300 bg-white px-5 py-8 text-center dark:border-slate-700 dark:bg-slate-900"
  >
    <h3 class="text-base font-semibold">Nothing recorded went unmatched</h3>
    <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
      Every request in the journal was served by a stub, so there is nothing here to explain. A
      request that has not been made yet cannot appear either — describe one in the other mode to
      score it before it is sent.
    </p>
  </div>
{:else}
  <div class="mt-4">
    <NearMissCandidates {groups} requestCaption="Recorded request that matched nothing" />
  </div>
{/if}
