<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { Dialog } from 'bits-ui';
  import type { Api } from '../api.svelte';

  /**
   * Where the admin token is asked for.
   *
   * It opens on a 401 and at no other time, which is what keeps the promise
   * that a deployment with no `admin_auth_token` never sees it: such a
   * deployment answers every admin call, so nothing ever asks.
   *
   * The dialog is bits-ui's rather than hand-written, and rather than the
   * platform's `<dialog showModal>`. Focus trapping, escape handling, restoring
   * focus to whatever opened it and `aria-modal` are the parts of a modal that
   * are easy to get subtly wrong, and the platform element implements all four —
   * but jsdom implements none of it, not even `showModal`, so a native dialog
   * would be a modal whose modal behaviour no test in this repository could
   * reach. A JS focus scope is testable where it runs.
   */
  interface Props {
    api: Api;
  }

  let { api }: Props = $props();

  let value = $state('');
  let input = $state<HTMLInputElement | null>(null);

  const canSubmit = $derived(value.trim() !== '');

  function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (!canSubmit) {
      return;
    }
    api.submitToken(value);
    // Dropped from component state as soon as it has been handed over. The
    // token's home is the api's session storage, and a copy left in a field
    // behind a closed dialog is a second place for it to be read from.
    value = '';
  }

  function handleClose() {
    api.dismissTokenRequest();
    value = '';
  }

  function focusTheField(event: Event) {
    // The default lands focus on the first tabbable element, which is close
    // enough to the field to be indistinguishable most of the time and is not
    // the field. Someone who has been asked for a token should be able to paste
    // it without pressing Tab first.
    event.preventDefault();
    input?.focus();
  }
</script>

<Dialog.Root
  bind:open={
    () => api.tokenRequested,
    (open) => {
      if (!open) handleClose();
    }
  }
>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-40 bg-slate-950/50" />
    <Dialog.Content
      onOpenAutoFocus={focusTheField}
      class="fixed top-1/2 left-1/2 z-50 w-[min(28rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900"
    >
      <Dialog.Title level={2} class="text-lg font-semibold tracking-tight">
        Admin token required
      </Dialog.Title>
      <Dialog.Description class="mt-2 text-sm text-slate-600 dark:text-slate-400">
        This deployment sets <code class="font-mono">admin_auth_token</code>, so the admin API
        answered 401. The token is kept for this browser tab only and sent as an
        <code class="font-mono">Authorization</code> header — never in a URL, a cookie, or storage that
        outlives the tab.
      </Dialog.Description>

      <form onsubmit={handleSubmit} class="mt-5">
        <label for="mockulus-admin-token" class="block text-sm font-medium">Token</label>
        <input
          bind:this={input}
          bind:value
          id="mockulus-admin-token"
          name="mockulus-admin-token"
          type="password"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-slate-900 focus:border-sky-500 focus:ring-2 focus:ring-sky-500/40 focus:outline-none dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
        />

        <div class="mt-5 flex justify-end gap-2">
          <!-- `type="button"` is load-bearing, not tidiness. A <button> with no
               type is a submit button, and this one comes first in the form, so
               it would be the form's default button: pressing Enter after
               pasting a token would cancel the dialog rather than send it. -->
          <Dialog.Close
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
          >
            Cancel
          </Dialog.Close>
          <button
            type="submit"
            disabled={!canSubmit}
            class="rounded-md bg-sky-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Use token
          </button>
        </div>
      </form>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
