<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusClient } from '@mockulus/admin-sdk';
  import ErrorState from './ErrorState.svelte';
  import OpsTypedConfirm from './OpsTypedConfirm.svelte';
  import { createAction } from '../action.svelte';
  import { getApi } from '../api.svelte';
  import { DANGER_ACTIONS, type DangerActionId } from '../ops-danger';

  /**
   * The three calls that destroy work belonging to people who are not in the
   * room.
   *
   * mockulus is built for a deployment several suites point at, and SPEC §1
   * tells users to namespace by URL prefix precisely so that they never have to
   * reset. Every button here contradicts that advice, which is why none of them
   * is one click: the guard is a sentence that has to be read and reproduced,
   * and the sentence is different for each so that having typed one does not
   * teach the fingers the next.
   *
   * What the confirmation shows is the blast radius in both directions — what
   * goes and what survives. The second half is not padding: "the journal, and
   * not your stubs" is the difference between a call somebody can take back and
   * one they cannot, and a warning that only lists damage gives a reader no way
   * to tell the three apart.
   */
  interface Props {
    /** Called after any of these lands, so the page's reads reflect what is left. */
    ondone: () => void;
  }

  let { ondone }: Props = $props();

  const api = getApi();

  /** Which confirmation is showing. At most one, because they are mutually exclusive questions. */
  let openId = $state<DangerActionId | undefined>(undefined);
  /** Which action is being run, so the other two do not all read as busy. */
  let running = $state<DangerActionId | undefined>(undefined);

  const perform = createAction(
    api,
    async (client: MockulusClient, id: DangerActionId): Promise<void> => {
      // Dispatched here rather than stored as a function on the table, because
      // the table is a description of the three calls and belongs in a module
      // with no client in it — which is also what makes it testable without a
      // network.
      switch (id) {
        case 'journal-clear':
          await client.requests.clear();
          return;
        case 'mappings-reset':
          await client.mappings.reset();
          return;
        case 'reset-all':
          await client.system.resetAll();
          return;
      }
    },
    () => {
      openId = undefined;
      running = undefined;
      ondone();
    },
  );

  function confirm(id: DangerActionId) {
    running = id;
    perform.run(id);
  }
</script>

<section aria-labelledby="ops-danger-heading">
  <h2
    id="ops-danger-heading"
    class="text-lg font-semibold tracking-tight text-rose-700 dark:text-rose-400"
  >
    Danger zone
  </h2>
  <p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    Every call below is <strong>deployment-wide</strong> and takes no filter. mockulus is built for
    an instance several suites share, so what these destroy is other people's work as well as yours,
    and the damage arrives in their results looking like a defect rather than like a reset somebody
    pressed. The supported way to clean up after a run is to tag its stubs with
    <code class="font-mono">metadata</code>
    and remove them with <code class="font-mono">remove-by-metadata</code>, which takes only what it
    was given.
  </p>

  {#if perform.error}
    <div class="mt-4">
      <ErrorState error={perform.error} onauthenticate={() => api.requestToken()} />
    </div>
  {/if}

  <ul class="mt-4 space-y-4">
    {#each DANGER_ACTIONS as action (action.id)}
      <li
        class="rounded-lg border border-rose-200 bg-rose-50/50 px-5 py-4 dark:border-rose-950 dark:bg-rose-950/20"
      >
        <div class="flex flex-wrap items-baseline justify-between gap-3">
          <div>
            <h3 class="text-base font-semibold">{action.label}</h3>
            <p class="mt-0.5 font-mono text-xs text-slate-600 dark:text-slate-400">
              {action.endpoint}
            </p>
          </div>
          <OpsTypedConfirm
            bind:open={
              () => openId === action.id, (next) => (openId = next ? action.id : undefined)
            }
            title={action.title}
            phrase={action.phrase}
            confirmLabel={action.label}
            busy={perform.pending && running === action.id}
            onconfirm={() => confirm(action.id)}
            triggerClass="rounded-md border border-rose-400 px-3 py-1.5 text-sm font-medium text-rose-700 hover:bg-rose-100 dark:border-rose-800 dark:text-rose-300 dark:hover:bg-rose-950/60"
          >
            {#snippet trigger()}{action.label}{/snippet}
            <!-- The same two lists as the card behind this dialog, repeated
                 rather than referred to. The dialog is modal: a screen-reader
                 user inside it cannot read the page it covers, and "as described
                 above" would be describing something they no longer have. -->
            <span class="block font-semibold text-rose-700 dark:text-rose-400">
              This destroys, for every caller of this deployment:
            </span>
            {#each action.destroys as line (line)}
              <span class="mt-1 block">— {line}</span>
            {/each}
            <span class="mt-3 block font-semibold">It leaves alone:</span>
            {#each action.keeps as line (line)}
              <span class="mt-1 block">— {line}</span>
            {/each}
          </OpsTypedConfirm>
        </div>

        <div class="mt-3 grid gap-3 text-sm sm:grid-cols-2">
          <div>
            <p
              class="text-xs font-semibold tracking-wide text-rose-700 uppercase dark:text-rose-400"
            >
              Destroys
            </p>
            <ul class="mt-1 space-y-1 text-slate-700 dark:text-slate-300">
              {#each action.destroys as line (line)}
                <li>{line}</li>
              {/each}
            </ul>
          </div>
          <div>
            <!-- slate-600 rather than the slate-500 this label wears elsewhere.
                 The card behind it is a tinted panel rather than white, and
                 slate-500 over rose-50/50 comes to 4.42:1 — the same colour that
                 clears the floor on every other surface in this UI fails on this
                 one. -->
            <p
              class="text-xs font-semibold tracking-wide text-slate-600 uppercase dark:text-slate-400"
            >
              Leaves alone
            </p>
            <ul class="mt-1 space-y-1 text-slate-700 dark:text-slate-300">
              {#each action.keeps as line (line)}
                <li>{line}</li>
              {/each}
            </ul>
          </div>
        </div>
      </li>
    {/each}
  </ul>

  <p class="mt-4 max-w-2xl text-xs text-slate-500 dark:text-slate-400">
    While the stub store is unavailable every one of these is refused with code 1020 and nothing is
    destroyed. Clearing the journal on a deployment that has it turned off — which is the default —
    answers code 1010 and is likewise a no-op.
  </p>
</section>
