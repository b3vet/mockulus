<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusClient, Scenario, StubMapping } from '@mockulus/admin-sdk';
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import ScenarioCard from '../lib/components/ScenarioCard.svelte';
  import { createAction } from '../lib/action.svelte';
  import { getApi } from '../lib/api.svelte';
  import { createResource } from '../lib/resource.svelte';
  import { membersOf, movedCount, STARTED } from '../lib/scenario-panel';
  import { MAX_LOADED_MAPPINGS } from '../lib/stubs';

  const api = getApi();

  /**
   * Stateful mocks, and the two questions a person on this page has: where is
   * each scenario now, and what happens if I move it.
   *
   * A scenario has no lifecycle to manage — it exists because some stub names
   * it, and its `possibleStates` are the states those stubs name plus `Started`
   * — so there is nothing here to create or delete. Everything on this page is
   * one of two writes: put this scenario in that state, or clear every stored
   * state at once.
   */
  const scenarios = createResource(api, (client) => client.scenarios.list());

  const listed = $derived<readonly Scenario[]>(scenarios.data?.scenarios ?? []);
  const moved = $derived(movedCount(listed));

  /**
   * The member stubs, which cost a second read and so are not taken until
   * somebody opens a breakdown.
   *
   * `GET /__admin/scenarios` deliberately does not embed member stubs the way
   * WireMock does (deviation #32), so there is no way to show which stub serves
   * in which state without reading the mappings. Making that read lazy rather
   * than skipping the feature is the compromise: the flag is what the load
   * function reads, so the resource's first automatic run costs nothing and the
   * 401-retry path is still the shared one rather than a second copy.
   */
  let membersWanted = $state(false);

  const mappings = createResource(
    api,
    async (client: MockulusClient): Promise<readonly StubMapping[] | undefined> => {
      if (!membersWanted) {
        return undefined;
      }
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

  function wantMembers() {
    if (membersWanted) {
      return;
    }
    membersWanted = true;
    mappings.reload();
  }

  /**
   * Which write is in flight, so the card that owns it can say "Setting…" on
   * the one button that was pressed rather than on all of them.
   *
   * Held beside the action rather than inside it because the action is shared:
   * one `createAction` gets the 401 retry, the supersede rule and the error
   * surface written once, and what it does not know is which of a dozen buttons
   * started it.
   */
  let inFlight = $state<{ name: string; state: string } | undefined>(undefined);

  const setState = createAction(
    api,
    async (client: MockulusClient, name: string, state: string): Promise<void> => {
      await client.scenarios.setState(name, state);
    },
    () => {
      inFlight = undefined;
      // Re-read rather than patching the card: another replica or another
      // suite may have moved something else since this page was drawn, and a
      // listing is one cheap call.
      scenarios.reload();
    },
  );

  function drive(name: string, state: string) {
    inFlight = { name, state };
    setState.run(name, state);
  }

  let confirmingReset = $state(false);

  const resetAllScenarios = createAction(
    api,
    async (client: MockulusClient): Promise<void> => {
      await client.scenarios.reset();
    },
    () => {
      confirmingReset = false;
      scenarios.reload();
    },
  );

  /** The failure to show, whichever of the two writes produced it. */
  const writeError = $derived(setState.error ?? resetAllScenarios.error);
</script>

<div class="flex flex-wrap items-baseline justify-between gap-3">
  <h1 class="text-2xl font-semibold tracking-tight">Scenarios</h1>
  <div class="flex flex-wrap items-center gap-2">
    <ConfirmDialog
      bind:open={confirmingReset}
      title="Return every scenario to Started?"
      confirmLabel="Reset every scenario"
      busy={resetAllScenarios.pending}
      onconfirm={() => resetAllScenarios.run()}
      triggerClass="rounded-md border border-rose-300 px-3 py-1.5 text-sm font-medium text-rose-700 hover:bg-rose-50 dark:border-rose-900 dark:text-rose-300 dark:hover:bg-rose-950/40"
    >
      {#snippet trigger()}Reset all scenarios{/snippet}
      <span class="block">
        <code class="font-mono">POST /__admin/scenarios/reset</code> deletes every stored scenario
        state in this deployment, so all {listed.length}
        {listed.length === 1 ? 'scenario reads' : 'scenarios read'} back as
        <code class="font-mono">Started</code>. It takes no filter and it is not scoped to you: a
        suite halfway through a flow on any of these scenarios will find itself back at the
        beginning, and its next assertion will fail for a reason that looks like a defect in the
        mock.
      </span>
      <span class="mt-2 block">
        No stub, file or setting is touched — only the states. To move one scenario, use its own
        Reset to Started instead.
      </span>
    </ConfirmDialog>
    <button
      type="button"
      onclick={() => scenarios.reload()}
      class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Refresh
    </button>
  </div>
</div>

<p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
  A scenario exists because a stub names it, and its possible states are the states those stubs name
  — plus <code class="font-mono">Started</code>, which every scenario has whether or not a stub
  names it, and which is where a reset puts it. There is nothing to create here and nothing to
  delete: removing the last stub that references a scenario is what ends it.
</p>

{#if writeError}
  <div class="mt-6">
    <ErrorState
      error={writeError}
      onretry={() => scenarios.reload()}
      onauthenticate={() => api.requestToken()}
    />
  </div>
{/if}

<div class="mt-6">
  {#if scenarios.error}
    <ErrorState
      error={scenarios.error}
      onretry={() => scenarios.reload()}
      onauthenticate={() => api.requestToken()}
    />
  {:else if scenarios.loading && scenarios.data === undefined}
    <p role="status" class="text-sm text-slate-600 dark:text-slate-400">Loading scenarios…</p>
  {:else if listed.length === 0}
    <div
      class="rounded-lg border border-slate-300 bg-white px-5 py-8 text-center dark:border-slate-700 dark:bg-slate-900"
    >
      <h2 class="text-base font-semibold">No scenarios</h2>
      <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
        No stub in this replica's snapshot carries a
        <code class="font-mono">scenarioName</code>. Register one that does — with
        <code class="font-mono">requiredScenarioState</code>,
        <code class="font-mono">newScenarioState</code> or both — and the scenario appears here with the
        states those stubs name.
      </p>
    </div>
  {:else}
    <p aria-live="polite" class="mb-4 text-sm text-slate-600 dark:text-slate-400">
      {listed.length}
      {listed.length === 1 ? 'scenario' : 'scenarios'},
      {#if moved === 0}
        all at <code class="font-mono">Started</code>.
      {:else}
        {moved} away from <code class="font-mono">Started</code>.
      {/if}
      {#if scenarios.loading}· refreshing…{/if}
    </p>

    <div class="space-y-4">
      {#each listed as item (item.id)}
        <ScenarioCard
          scenario={item}
          members={mappings.data === undefined ? undefined : membersOf(mappings.data, item.name)}
          membersError={mappings.error}
          pendingState={setState.pending && inFlight?.name === item.name
            ? inFlight.state
            : undefined}
          anyPending={setState.pending}
          onset={(state) => drive(item.name, state)}
          onexpand={wantMembers}
        />
      {/each}
    </div>

    <p class="mt-6 max-w-2xl text-xs text-slate-500 dark:text-slate-400">
      Setting a state is <code class="font-mono">PUT /__admin/scenarios/&#123;name&#125;/state</code
      >, which is validated against what the stubs define: a state no stub names is refused with
      code 1031 rather than accepted. <code class="font-mono">{STARTED}</code> is the exception in the
      other direction — it is a possible state of every scenario here and is always accepted, where WireMock
      refuses it unless a stub names it.
    </p>
  {/if}
</div>
