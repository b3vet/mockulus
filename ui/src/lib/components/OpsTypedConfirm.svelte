<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { Dialog } from 'bits-ui';
  import type { Snippet } from 'svelte';
  import { phraseMatches } from '../ops-danger';

  /**
   * The question asked before something belonging to other people is destroyed.
   *
   * This is not `ConfirmDialog`, and the difference is the gate rather than the
   * decoration. There, the confirm button is live from the moment the dialog
   * opens and focus is parked on Cancel so the reflex answer is the harmless
   * one; that is the right shape for deleting the one stub whose page you are
   * standing on. Here there is nothing on screen that identifies what is about
   * to go, because what is about to go is everybody's — so the confirm button
   * has to be dead until a specific sentence has been typed, and `busy` in the
   * shared dialog means "a call is in flight" and renders "Working…", which
   * would say the wrong thing about a button waiting on a phrase. Reusing it
   * would have meant overloading that flag to mean two different things.
   *
   * Everything the shared dialog gets right is kept and kept the same way: the
   * trigger is rendered here as the dialog's own child, which is what gives
   * focus somewhere to return to when the dialog closes, and bits-ui rather than
   * the platform `<dialog>` because jsdom implements none of the platform
   * element's modal behaviour and a test could not reach it.
   *
   * Focus lands on the text input rather than on Cancel. The reflex Enter is
   * already harmless — the confirm button is disabled until the phrase matches —
   * and putting the cursor where the work is means a keyboard user is not
   * hunting for the one control that unlocks the dialog.
   */
  interface Props {
    open: boolean;
    title: string;
    /** The sentence that must be typed. Different per action, so one does not train the next. */
    phrase: string;
    confirmLabel: string;
    /** A call is in flight; the controls are held until it answers. */
    busy?: boolean;
    onconfirm: () => void;
    trigger: Snippet;
    triggerClass?: string;
    /** The blast radius: what goes, and what survives. */
    children: Snippet;
  }

  let {
    open = $bindable(),
    title,
    phrase,
    confirmLabel,
    busy = false,
    onconfirm,
    trigger,
    triggerClass = '',
    children,
  }: Props = $props();

  let typed = $state('');
  let field = $state<HTMLInputElement | null>(null);

  const matches = $derived(phraseMatches(typed, phrase));

  function focusField(event: Event) {
    event.preventDefault();
    field?.focus();
  }

  // Cleared on close rather than on open, so that a dialog dismissed by Escape
  // and reopened does not arrive already unlocked by a phrase typed minutes ago.
  function onOpenChange(next: boolean) {
    if (!next) {
      typed = '';
    }
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (matches && !busy) {
      onconfirm();
    }
  }
</script>

<Dialog.Root bind:open {onOpenChange}>
  <Dialog.Trigger class={triggerClass}>
    {@render trigger()}
  </Dialog.Trigger>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-40 bg-slate-950/50" />
    <Dialog.Content
      onOpenAutoFocus={focusField}
      class="fixed top-1/2 left-1/2 z-50 w-[min(36rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-rose-300 bg-white p-6 shadow-xl dark:border-rose-900 dark:bg-slate-900"
    >
      <Dialog.Title level={2} class="text-lg font-semibold tracking-tight">
        {title}
      </Dialog.Title>
      <Dialog.Description class="mt-2 text-sm text-slate-600 dark:text-slate-400">
        {@render children()}
      </Dialog.Description>

      <form onsubmit={submit} class="mt-5">
        <label for="typed-confirmation" class="block text-sm font-medium">
          Type <code class="font-mono font-semibold">{phrase}</code> to confirm
        </label>
        <input
          bind:this={field}
          bind:value={typed}
          id="typed-confirmation"
          type="text"
          autocomplete="off"
          spellcheck="false"
          disabled={busy}
          class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm disabled:opacity-50 dark:border-slate-700 dark:bg-slate-950"
        />

        <div class="mt-6 flex justify-end gap-2">
          <button
            type="button"
            disabled={busy}
            onclick={() => (open = false)}
            class="rounded-md px-3 py-1.5 text-sm font-medium text-slate-600 hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:text-slate-400 dark:hover:bg-slate-800"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={busy || !matches}
            class="rounded-md bg-rose-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-rose-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy ? 'Working…' : confirmLabel}
          </button>
        </div>
      </form>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
