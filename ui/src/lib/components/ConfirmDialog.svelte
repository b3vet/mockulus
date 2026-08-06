<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { Dialog } from 'bits-ui';
  import type { Snippet } from 'svelte';

  /**
   * The question asked before something is destroyed.
   *
   * bits-ui's dialog rather than a hand-written one, on the same argument
   * `TokenSheet` records: focus trapping, Escape, restoring focus to whatever
   * opened it and `aria-modal` are the four parts of a modal that are easy to
   * get subtly wrong, and jsdom implements none of the platform `<dialog>`'s
   * version of them — so a native dialog would be a modal whose modal behaviour
   * no test here could reach.
   *
   * What this adds over the dialog is where focus lands. The default is the
   * first tabbable element, which in a confirm/cancel pair reads left to right
   * and would put a keyboard user's Enter on the destructive button. Focus goes
   * to Cancel instead, so the reflex answer to a dialog nobody read is the one
   * that changes nothing.
   *
   * The control that opens it is rendered here, as the dialog's own trigger,
   * rather than being a button elsewhere on the page that sets `open`. That is
   * what gives focus somewhere to return to when the dialog closes: the trigger
   * is the element bits-ui restores to, and a dialog opened by a button it does
   * not know about drops the keyboard user back at the top of the document. It
   * cannot be left to the browser either — clicking a button focuses it in
   * Chrome and does not in Safari, so on half the browsers there would be
   * nothing to restore.
   */
  interface Props {
    /** Whether the dialog is showing. Two-way, so Escape and the overlay close it. */
    open: boolean;
    title: string;
    /** The label on the button that goes through with it. */
    confirmLabel: string;
    /** A call is in flight; the buttons are held until it answers. */
    busy?: boolean;
    onconfirm: () => void;
    /** What the opening control says. Rendered inside the trigger button. */
    trigger: Snippet;
    /** Classes for the trigger, so it can sit in a row of page controls. */
    triggerClass?: string;
    /** The question itself, and anything else the reader needs to answer it. */
    children: Snippet;
  }

  let {
    open = $bindable(),
    title,
    confirmLabel,
    busy = false,
    onconfirm,
    trigger,
    triggerClass = '',
    children,
  }: Props = $props();

  let cancel = $state<HTMLButtonElement | null>(null);

  function focusCancel(event: Event) {
    event.preventDefault();
    cancel?.focus();
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Trigger class={triggerClass}>
    {@render trigger()}
  </Dialog.Trigger>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-40 bg-slate-950/50" />
    <Dialog.Content
      onOpenAutoFocus={focusCancel}
      class="fixed top-1/2 left-1/2 z-50 w-[min(32rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900"
    >
      <Dialog.Title level={2} class="text-lg font-semibold tracking-tight">
        {title}
      </Dialog.Title>
      <Dialog.Description class="mt-2 text-sm text-slate-600 dark:text-slate-400">
        {@render children()}
      </Dialog.Description>

      <div class="mt-6 flex justify-end gap-2">
        <button
          bind:this={cancel}
          type="button"
          disabled={busy}
          onclick={() => (open = false)}
          class="rounded-md px-3 py-1.5 text-sm font-medium text-slate-600 hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:text-slate-400 dark:hover:bg-slate-800"
        >
          Cancel
        </button>
        <button
          type="button"
          disabled={busy}
          onclick={onconfirm}
          class="rounded-md bg-rose-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-rose-700 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? 'Working…' : confirmLabel}
        </button>
      </div>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
