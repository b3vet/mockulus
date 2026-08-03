<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { Scenario, StubMapping } from '@mockulus/admin-sdk';
  import AppLink from './AppLink.svelte';
  import ErrorState from './ErrorState.svelte';
  import { describeMembership, isAtStart, isCurrent, servingIn, STARTED } from '../scenario-panel';
  import { methodOf, stubIdOf, urlCriterionOf } from '../stubs';
  import { toHref } from '../router';
  import { getRouter } from '../router.svelte';

  /**
   * One scenario: where it is now, and every state it can be driven to in one
   * click.
   *
   * Two things about this surface are mockulus deviations from WireMock, and
   * both are visible here rather than only in the docs.
   *
   * `Started` is a possible state of **every** scenario, so the button for it is
   * always offered and the server always accepts it (deviation #34). WireMock
   * derives `possibleStates` from the stubs alone and refuses to set `Started`
   * when no stub names it — even though it is the state the scenario begins in
   * and the state a reset returns it to. Anyone who has met that refusal will
   * expect this button to be missing, which is why the panel says so.
   *
   * The member stubs are a **separate query**, because `GET /__admin/scenarios`
   * does not embed them the way WireMock does (deviation #32). That is why they
   * arrive on demand rather than with the card: a page of cards should not cost
   * a mappings read that most visits never look at.
   */
  interface Props {
    scenario: Scenario;
    /** The scenario's stubs, or `undefined` while the separate query has not run. */
    members: readonly StubMapping[] | undefined;
    membersError: unknown;
    /** The state currently being written on this card, so only its own button reads as busy. */
    pendingState: string | undefined;
    /** Any state write is in flight; every button on every card is held until it answers. */
    anyPending: boolean;
    onset: (state: string) => void;
    /** Called when the breakdown is opened, which is what triggers the separate query. */
    onexpand: () => void;
  }

  let { scenario, members, membersError, pendingState, anyPending, onset, onexpand }: Props =
    $props();

  const router = getRouter();

  const atStart = $derived(isAtStart(scenario));

  /**
   * Whether the per-state breakdown is showing.
   *
   * A button and a conditional rather than `<details>`, because opening it has
   * a side effect — the mappings read — and `<details>` reports open and close
   * through the same event. Owning the state here means the read is asked for
   * exactly when somebody opens the breakdown and never when they close it.
   */
  let expanded = $state(false);

  function toggle() {
    expanded = !expanded;
    if (expanded) {
      onexpand();
    }
  }
</script>

<article
  class="rounded-lg border border-slate-200 bg-white px-5 py-4 dark:border-slate-800 dark:bg-slate-900"
>
  <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
    <h2 class="text-base font-semibold break-all">{scenario.name}</h2>
    <p class="text-sm text-slate-600 dark:text-slate-400">
      now in
      <span
        class="ml-1 rounded-full bg-sky-100 px-2 py-0.5 font-mono text-xs font-semibold text-sky-900 dark:bg-sky-950 dark:text-sky-200"
        >{scenario.state}</span
      >
    </p>
  </div>

  <div class="mt-3 flex flex-wrap items-center gap-2">
    {#each scenario.possibleStates as state (state)}
      {#if isCurrent(scenario, state)}
        <!-- The current state is not a button. A disabled control in a row of
             enabled ones is a tab stop that answers nothing, and the state is
             already named above; this is the marker in the row, not a second
             way to reach it. -->
        <span
          class="rounded-md border border-sky-300 bg-sky-50 px-3 py-1.5 text-sm font-medium text-sky-900 dark:border-sky-900 dark:bg-sky-950/50 dark:text-sky-200"
        >
          {state} · current
        </span>
      {:else}
        <button
          type="button"
          aria-label="Set {scenario.name} to {state}"
          disabled={anyPending}
          onclick={() => onset(state)}
          class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:hover:bg-slate-800"
        >
          {pendingState === state ? 'Setting…' : state}
        </button>
      {/if}
    {/each}

    <!-- The reset is the same write with `Started` as its target: there is no
         per-scenario reset endpoint, and inventing the distinction in the UI
         would suggest one exists. It earns its own button because "put this
         flow back to the beginning" is what somebody is actually asking for,
         and they should not have to know that is what `Started` means. -->
    <button
      type="button"
      aria-label="Reset {scenario.name} to {STARTED}"
      disabled={anyPending || atStart}
      onclick={() => onset(STARTED)}
      class="rounded-md px-3 py-1.5 text-sm font-medium text-slate-600 underline underline-offset-4 hover:text-slate-900 disabled:cursor-not-allowed disabled:no-underline disabled:opacity-40 dark:text-slate-400 dark:hover:text-slate-200"
    >
      {atStart ? 'Already at Started' : 'Reset to Started'}
    </button>
  </div>

  <button
    type="button"
    aria-expanded={expanded}
    onclick={toggle}
    class="mt-4 text-sm text-slate-600 underline underline-offset-4 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-200"
  >
    Which stubs serve in each state
  </button>

  {#if expanded}
    <p class="mt-2 max-w-2xl text-xs text-slate-500 dark:text-slate-400">
      Read from <code class="font-mono">GET /__admin/mappings</code>, not from the scenario listing:
      mockulus deliberately does not embed member stubs in
      <code class="font-mono">GET /__admin/scenarios</code> the way WireMock does, so this is a second
      query and it runs only when you open this. A stub with no required state is eligible in every state.
      Eligible is not chosen — priority and the rest of the request criteria still decide between two
      stubs that both qualify.
    </p>

    {#if membersError}
      <div class="mt-3">
        <ErrorState error={membersError} />
      </div>
    {:else if members === undefined}
      <p role="status" class="mt-3 text-sm text-slate-600 dark:text-slate-400">
        Reading the mappings…
      </p>
    {:else if members.length === 0}
      <p class="mt-3 text-sm text-slate-600 dark:text-slate-400">
        No stub in the mappings that were read names this scenario. A scenario exists because a stub
        names it, so this means the listing was capped before reaching them.
      </p>
    {:else}
      <ul class="mt-3 space-y-3 text-sm">
        {#each scenario.possibleStates as state (state)}
          {@const eligible = servingIn(members, state)}
          <li>
            <p class="font-mono text-xs font-semibold text-slate-500 dark:text-slate-400">
              {state}
              {#if isCurrent(scenario, state)}· current{/if}
            </p>
            {#if eligible.length === 0}
              <p class="mt-1 text-slate-600 dark:text-slate-400">
                No stub in this scenario serves here — a request that would have matched falls
                through to the rest of the snapshot.
              </p>
            {:else}
              <ul class="mt-1 space-y-1">
                {#each eligible as mapping, index (stubIdOf(mapping) ?? index)}
                  {@const id = stubIdOf(mapping)}
                  {@const criterion = urlCriterionOf(mapping)}
                  <li class="flex flex-wrap items-baseline gap-x-2">
                    <span class="font-mono text-xs font-semibold">{methodOf(mapping)}</span>
                    {#if id}
                      <AppLink
                        href={toHref(`/stubs/${encodeURIComponent(id)}`)}
                        onnavigate={(href) => router.navigate(href)}
                        class="font-mono break-all text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
                      >
                        {criterion ? criterion.value : 'any URL'}
                      </AppLink>
                    {:else}
                      <span class="font-mono break-all"
                        >{criterion ? criterion.value : 'any URL'}</span
                      >
                    {/if}
                    <span class="text-xs text-slate-500 dark:text-slate-400">
                      {describeMembership(mapping)}
                    </span>
                  </li>
                {/each}
              </ul>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</article>
