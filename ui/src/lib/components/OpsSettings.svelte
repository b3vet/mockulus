<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusClient, Settings, SettingsEnvelope } from '@mockulus/admin-sdk';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import ErrorState from './ErrorState.svelte';
  import { createAction } from '../action.svelte';
  import { getApi } from '../api.svelte';
  import {
    draftFrom,
    emptyDraft,
    isUnset,
    summarize,
    toSettings,
    type DraftField,
    type SettingsDraft,
  } from '../ops-settings';
  import type { Resource } from '../resource.svelte';

  /**
   * The deployment-wide response delay, which is the whole of the supported
   * settings surface.
   *
   * The one property of this endpoint that has to survive into the UI is that
   * `POST /__admin/settings` **replaces** the document rather than merging it.
   * A form that sent only the field somebody touched would clear the other one
   * silently, so this always sends both halves and says so above the button.
   * Clearing is therefore not a separate operation — it is this same call with
   * an empty document — but it gets its own control because "put the deployment
   * back the way I found it" is a thing people want to do deliberately and
   * should not have to arrive at by emptying three boxes.
   *
   * This is a shared surface. The delay applies to every matched response in the
   * deployment, not to the caller who set it, so the copy names that rather than
   * leaving it to be discovered.
   */
  interface Props {
    settings: Resource<SettingsEnvelope>;
  }

  let { settings }: Props = $props();

  const api = getApi();

  let draft = $state<SettingsDraft>(emptyDraft());
  /** The field the draft was refused on, if any, so the message sits beside its input. */
  let refusal = $state<{ field: DraftField; message: string } | undefined>(undefined);
  let saved = $state(false);

  /**
   * Whether the form has been filled from the server yet.
   *
   * Not reactive state, and deliberately: a refresh that returns the same
   * document must not overwrite edits somebody is in the middle of making. The
   * seed happens on the first document to arrive and after a write of our own,
   * both of which are moments where the form and the server are meant to agree.
   */
  let seeded = false;

  $effect(() => {
    const envelope = settings.data;
    if (envelope && !seeded) {
      seeded = true;
      draft = draftFrom(envelope.settings);
    }
  });

  const save = createAction(
    api,
    async (client: MockulusClient, document: Settings): Promise<void> => {
      await client.settings.update(document);
    },
    () => {
      saved = true;
      // Re-seeded from the server rather than from the draft: the stored
      // document is the authority on what is in force, and reading it back is
      // what makes that visible instead of assumed.
      seeded = false;
      settings.reload();
    },
  );

  function submit(event: SubmitEvent) {
    event.preventDefault();
    saved = false;
    const result = toSettings(draft);
    if (!result.ok) {
      refusal = { field: result.field, message: result.message };
      return;
    }
    refusal = undefined;
    save.run(result.settings);
  }

  let confirmingClear = $state(false);

  const clear = createAction(
    api,
    async (client: MockulusClient): Promise<void> => {
      await client.settings.update({});
    },
    () => {
      confirmingClear = false;
      saved = true;
      refusal = undefined;
      seeded = false;
      settings.reload();
    },
  );

  const stored = $derived(settings.data?.settings);
  const busy = $derived(save.pending || clear.pending);
  const problem = $derived(save.error ?? clear.error);
</script>

<section aria-labelledby="ops-settings-heading">
  <h2 id="ops-settings-heading" class="text-lg font-semibold tracking-tight">Global settings</h2>
  <p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    The deployment-wide response delay, and the whole of the supported settings surface — WireMock's
    others are refused by name rather than accepted and ignored. It is stored and epoch-bumped, so
    it reaches every replica and survives a restart, and it applies to every matched response in the
    deployment whose stub declares no delay of its own.
  </p>

  {#if settings.error}
    <div class="mt-3">
      <ErrorState
        error={settings.error}
        onretry={() => settings.reload()}
        onauthenticate={() => api.requestToken()}
      />
    </div>
  {:else if settings.data === undefined}
    <p role="status" class="mt-3 text-sm text-slate-600 dark:text-slate-400">Reading settings…</p>
  {:else}
    <p class="mt-3 text-sm text-slate-700 dark:text-slate-300">{summarize(stored)}</p>

    <form onsubmit={submit} class="mt-4 max-w-xl space-y-5">
      <div>
        <label for="settings-fixed-delay" class="block text-sm font-medium">
          Fixed delay (milliseconds)
        </label>
        <input
          id="settings-fixed-delay"
          type="text"
          inputmode="numeric"
          bind:value={draft.fixedDelay}
          aria-describedby="settings-fixed-delay-help{refusal?.field === 'fixedDelay'
            ? ' settings-fixed-delay-error'
            : ''}"
          aria-invalid={refusal?.field === 'fixedDelay'}
          class="mt-1.5 w-40 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
        />
        <p id="settings-fixed-delay-help" class="mt-1 text-xs text-slate-500 dark:text-slate-400">
          Leave empty for none.
        </p>
        {#if refusal?.field === 'fixedDelay'}
          <p
            id="settings-fixed-delay-error"
            role="alert"
            class="mt-1 text-xs text-rose-700 dark:text-rose-400"
          >
            {refusal.message}
          </p>
        {/if}
      </div>

      <fieldset>
        <legend class="text-sm font-medium">Sampled delay</legend>
        <p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
          Added on top of the fixed delay, resampled per matched response.
        </p>
        <div class="mt-2 space-y-2">
          <label class="flex items-center gap-2 text-sm">
            <input type="radio" value="none" bind:group={draft.kind} name="delay-distribution" />
            None
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input type="radio" value="uniform" bind:group={draft.kind} name="delay-distribution" />
            Uniform
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input
              type="radio"
              value="lognormal"
              bind:group={draft.kind}
              name="delay-distribution"
            />
            Log-normal
          </label>
        </div>

        {#if draft.kind === 'uniform'}
          <div class="mt-3 flex flex-wrap gap-4">
            <div>
              <label for="settings-lower" class="block text-sm font-medium">Lower (ms)</label>
              <input
                id="settings-lower"
                type="text"
                inputmode="numeric"
                bind:value={draft.lower}
                aria-invalid={refusal?.field === 'lower'}
                class="mt-1.5 w-32 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
            </div>
            <div>
              <label for="settings-upper" class="block text-sm font-medium">Upper (ms)</label>
              <input
                id="settings-upper"
                type="text"
                inputmode="numeric"
                bind:value={draft.upper}
                aria-invalid={refusal?.field === 'upper'}
                class="mt-1.5 w-32 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
            </div>
          </div>
        {:else if draft.kind === 'lognormal'}
          <div class="mt-3 flex flex-wrap gap-4">
            <div>
              <label for="settings-median" class="block text-sm font-medium">Median (ms)</label>
              <input
                id="settings-median"
                type="text"
                inputmode="numeric"
                bind:value={draft.median}
                aria-invalid={refusal?.field === 'median'}
                class="mt-1.5 w-32 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
            </div>
            <div>
              <label for="settings-sigma" class="block text-sm font-medium">Sigma</label>
              <input
                id="settings-sigma"
                type="text"
                inputmode="decimal"
                bind:value={draft.sigma}
                aria-invalid={refusal?.field === 'sigma'}
                class="mt-1.5 w-32 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
            </div>
          </div>
        {/if}

        {#if refusal && refusal.field !== 'fixedDelay'}
          <p role="alert" class="mt-2 text-xs text-rose-700 dark:text-rose-400">
            {refusal.message}
          </p>
        {/if}
      </fieldset>

      <p class="text-xs text-slate-500 dark:text-slate-400">
        Saving <strong>replaces</strong> the stored document rather than merging into it, so what is on
        this form is what the deployment will hold.
      </p>

      <div class="flex flex-wrap items-center gap-2">
        <button
          type="submit"
          disabled={busy}
          class="rounded-md bg-sky-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-800 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {save.pending ? 'Saving…' : 'Save settings'}
        </button>
        <ConfirmDialog
          bind:open={confirmingClear}
          title="Clear the global settings?"
          confirmLabel="Clear the settings"
          busy={clear.pending}
          onconfirm={() => clear.run()}
          triggerClass="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
        >
          {#snippet trigger()}Clear settings{/snippet}
          <span class="block">
            This posts an empty document, which is how a deployment is put back the way it was
            found. Every matched response in the deployment stops waiting out the delay — including
            for suites that are relying on it right now.
          </span>
          <span class="mt-2 block">No stub, file, journal entry or scenario state is touched.</span>
        </ConfirmDialog>
        {#if saved && !problem}
          <span role="status" class="text-sm text-emerald-700 dark:text-emerald-400">
            Saved. The document above was read back from the server.
          </span>
        {/if}
      </div>
    </form>

    {#if isUnset(stored)}
      <p class="mt-3 max-w-2xl text-xs text-slate-500 dark:text-slate-400">
        Nothing has ever been written here, which is what zero-config looks like: the endpoint
        answers an empty document rather than a 404 or an invented default.
      </p>
    {/if}

    {#if problem}
      <div class="mt-4">
        <ErrorState error={problem} onauthenticate={() => api.requestToken()} />
      </div>
    {/if}
  {/if}
</section>
