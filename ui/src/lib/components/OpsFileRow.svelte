<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { StubMapping } from '@mockulus/admin-sdk';
  import AppLink from './AppLink.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { toHref } from '../router';
  import { getRouter } from '../router.svelte';
  import { methodOf, stubIdOf, urlCriterionOf } from '../stubs';

  /**
   * One stored response-body file, and the stubs pointing at it.
   *
   * A row is its own component because the delete confirmation is: the dialog
   * restores focus to the trigger that opened it, which means the trigger has to
   * be the dialog's own child, which means one dialog per row. Holding the open
   * flag here rather than a name in the panel above keeps that a local fact.
   *
   * The back-links are the reason this row is worth more than a name in a list.
   * Deleting a file does not fail the stubs that reference it — they keep
   * loading and start serving 500 with code 1022 on the next rebuild — so the
   * damage from deleting the wrong file is invisible until traffic arrives.
   * Naming the stubs before the deletion is what makes it visible in time.
   */
  interface Props {
    name: string;
    references: readonly StubMapping[];
    /** A file call is in flight somewhere on the panel; this row's controls wait for it. */
    busy: boolean;
    ondownload: (name: string) => void;
    ondelete: (name: string) => void;
  }

  let { name, references, busy, ondownload, ondelete }: Props = $props();

  const router = getRouter();

  let confirming = $state(false);
</script>

<li class="flex flex-wrap items-baseline gap-x-3 gap-y-2 px-4 py-3">
  <span class="font-mono text-sm break-all">{name}</span>

  <span class="text-xs text-slate-500 dark:text-slate-400">
    {#if references.length === 0}
      referenced by no stub
    {:else}
      referenced by {references.length}
      {references.length === 1 ? 'stub' : 'stubs'}
    {/if}
  </span>

  <span class="ml-auto flex flex-wrap items-center gap-2">
    <button
      type="button"
      aria-label="Download {name}"
      disabled={busy}
      onclick={() => ondownload(name)}
      class="rounded-md border border-slate-300 px-2.5 py-1 text-xs font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Download
    </button>
    <ConfirmDialog
      bind:open={confirming}
      title="Delete this response-body file?"
      confirmLabel="Delete the file"
      {busy}
      onconfirm={() => ondelete(name)}
      triggerClass="rounded-md border border-rose-300 px-2.5 py-1 text-xs font-medium text-rose-700 hover:bg-rose-50 dark:border-rose-900 dark:text-rose-300 dark:hover:bg-rose-950/40"
    >
      {#snippet trigger()}Delete{/snippet}
      <span class="block">
        <code class="font-mono break-all">{name}</code> is removed from the deployment's file store. The
        store holds one copy for every replica, so this is not local to the pod that answers it.
      </span>
      {#if references.length === 0}
        <span class="mt-2 block">
          No stub in the mappings that were read names this file, so nothing that is registered
          today will change behaviour.
        </span>
      {:else}
        <span class="mt-2 block">
          {references.length}
          {references.length === 1 ? 'stub references' : 'stubs reference'} it. They will keep loading
          and start answering 500 with code 1022 on the next snapshot rebuild — a deleted file does not
          take a deployment's stubs down with it, which is exactly why the breakage will not announce
          itself until a request arrives.
        </span>
      {/if}
      <span class="mt-2 block">There is no undo; the bytes are not kept anywhere else.</span>
    </ConfirmDialog>
  </span>

  {#if references.length > 0}
    <ul class="w-full space-y-1">
      {#each references as mapping, index (stubIdOf(mapping) ?? index)}
        {@const id = stubIdOf(mapping)}
        {@const criterion = urlCriterionOf(mapping)}
        <li class="flex flex-wrap items-baseline gap-x-2 text-xs">
          <span class="font-mono font-semibold">{methodOf(mapping)}</span>
          {#if id}
            <AppLink
              href={toHref(`/stubs/${encodeURIComponent(id)}`)}
              onnavigate={(href) => router.navigate(href)}
              class="font-mono break-all text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
            >
              {criterion ? criterion.value : 'any URL'}
            </AppLink>
          {:else}
            <span class="font-mono break-all">{criterion ? criterion.value : 'any URL'}</span>
          {/if}
          {#if mapping.name}
            <span class="text-slate-500 dark:text-slate-400">{mapping.name}</span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</li>
