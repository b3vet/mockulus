<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusProblem } from '@mockulus/admin-sdk';
  import type { DocumentSpan, PointerResolution } from '../json-pointer';

  /**
   * Every problem the server found, each one a link into the document.
   *
   * This is the point of the whole error design, seen from the outside. mockulus
   * validates a mapping to the end before answering, so a document with three
   * unsupported matchers comes back as one 422 carrying three entries, each with
   * the JSON Pointer of the field that caused it (SPEC §2 P3, Appendix B). Two
   * halves of that are wasted if the reader has to act on it by hand: the list
   * is only better than one-error-at-a-time if all of it is on screen, and a
   * pointer is only better than a prose description if it takes you there.
   *
   * So each row resolves its pointer against the document as it stands *now*,
   * and a row whose pointer no longer names anything says so rather than
   * offering a control that would do nothing. That distinction has to be live
   * rather than computed once: the user fixes the first field, which moves every
   * offset after it, and may delete the field the second problem names.
   */
  interface Props {
    problems: readonly MockulusProblem[];
    /**
     * Resolves a pointer against the current document. Reading the document
     * inside this closure is what makes the rows re-evaluate as it is edited.
     */
    locate: (pointer: string) => PointerResolution;
    /** Moves the editor's cursor and selection to a resolved span. */
    reveal: (span: DocumentSpan) => void;
  }

  let { problems, locate, reveal }: Props = $props();

  /**
   * The rows, with each pointer resolved. Derived rather than computed on click
   * so a pointer that cannot be resolved is visible before it is pressed —
   * "nothing happened" is the failure this component exists to prevent.
   */
  const rows = $derived(
    problems.map((problem) => {
      const pointer = problem.source?.pointer;
      return {
        problem,
        pointer,
        located: pointer === undefined ? undefined : locate(pointer),
      };
    }),
  );

  /** What the last jump did, announced for a reader who cannot see the cursor move. */
  let outcome = $state('');

  function jump(pointer: string) {
    const located = locate(pointer);
    if (located.resolved) {
      reveal(located);
      outcome = `Moved the cursor to ${pointer}.`;
      return;
    }
    // Re-resolved on the click rather than trusting the row's own answer,
    // because the document may have changed since it was rendered. Both paths
    // say what happened; neither leaves the press unexplained.
    outcome =
      located.reason === 'pointer-syntax'
        ? `${pointer} is not a JSON Pointer, so there is nowhere to go.`
        : `${pointer} is not in this document.`;
  }

  function unresolvedNote(located: PointerResolution): string {
    if (located.resolved) {
      return '';
    }
    if (located.reason === 'pointer-syntax') {
      return 'The server did not send a JSON Pointer here, so there is no field to jump to.';
    }
    return located.matched === ''
      ? 'Not in this document — nothing of this pointer resolves against the text above.'
      : `Not in this document. The deepest part that does resolve is ${located.matched}.`;
  }
</script>

<ul aria-label="Problems the server reported" class="space-y-2">
  {#each rows as row, index (index)}
    <li
      class="rounded-lg border border-rose-300 bg-rose-50/70 px-4 py-3 text-sm text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100"
    >
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span class="font-mono text-xs opacity-70">code {row.problem.code}</span>
        <span class="font-semibold">{row.problem.title ?? 'Refused'}</span>
        {#if row.located?.resolved}
          <button
            type="button"
            onclick={() => row.pointer !== undefined && jump(row.pointer)}
            class="ms-auto rounded-md border border-current/30 px-2.5 py-1 text-xs font-medium hover:bg-white/70 focus:outline-2 focus:outline-offset-2 focus:outline-current dark:hover:bg-black/30"
          >
            Go to <code class="font-mono">{row.pointer}</code>
          </button>
        {/if}
      </div>

      <p class="mt-1.5">{row.problem.detail ?? 'The server gave no further detail.'}</p>

      {#if row.pointer === undefined}
        <p class="mt-1.5 text-xs opacity-80">
          This problem is about the document as a whole, so the server named no field.
        </p>
      {:else if row.located && !row.located.resolved}
        <p class="mt-1.5 text-xs opacity-80">
          <code class="font-mono">{row.pointer}</code> — {unresolvedNote(row.located)}
        </p>
      {/if}
    </li>
  {/each}
</ul>

<!-- Visible rather than screen-reader-only. A jump that lands is obvious to
     someone watching the editor and invisible to someone watching the list, and
     a jump that finds nothing is invisible to everybody. -->
<p role="status" class="mt-2 min-h-5 text-xs text-slate-600 dark:text-slate-400">{outcome}</p>
