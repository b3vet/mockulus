<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import {
    isMockulusError,
    type MockulusClient,
    type MockulusProblem,
    type StubMappingImport,
  } from '@mockulus/admin-sdk';
  import { createAction } from '../action.svelte';
  import { getApi } from '../api.svelte';
  import ErrorState from './ErrorState.svelte';
  import { importPointerParts, parseImportDocument } from '../mappings-file';
  import { methodOf, urlCriterionOf } from '../stubs';

  /**
   * Loading a `{"mappings": [...]}` file into the deployment.
   *
   * The import endpoint is **atomic**: it compiles every mapping in the batch
   * before it writes any of them, so a file with one bad stub in forty leaves the
   * deployment exactly as it was (`internal/admin/mappings_write.go`). That is
   * the property this panel is built around, because it is the one a reader will
   * otherwise assume the opposite of. Somebody who submits forty stubs and sees a
   * list of errors will go looking for which of them landed, and the answer —
   * none — has to be the first thing on screen rather than something to work out.
   *
   * The second thing the failure surface owes them is *which* mapping. The
   * server prefixes every pointer with the element's index, so the problems are
   * grouped back into the mappings they came from and each group is labelled with
   * the stub it names.
   */
  interface Props {
    /** Called after a batch is written, so the list behind this panel re-reads. */
    onimported: () => void;
  }

  let { onimported }: Props = $props();

  const api = getApi();

  /** The chosen file, once it has been read and understood. */
  let chosenName = $state<string | undefined>(undefined);
  let batch = $state<StubMappingImport | undefined>(undefined);
  let count = $state(0);
  /** Why the chosen file cannot be sent. Set instead of `batch`, never with it. */
  let fileProblem = $state<string | undefined>(undefined);
  let written = $state<number | undefined>(undefined);

  const load = createAction(
    api,
    async (client: MockulusClient, document: StubMappingImport): Promise<number> => {
      await client.mappings.import(document);
      return document.mappings?.length ?? 0;
    },
    (imported) => {
      written = imported;
      batch = undefined;
      onimported();
    },
  );

  function clear() {
    chosenName = undefined;
    batch = undefined;
    count = 0;
    fileProblem = undefined;
    written = undefined;
    load.reset();
  }

  async function choose(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    // Reset the control itself, so choosing the same file twice — after fixing
    // it on disk — still fires a change event.
    input.value = '';
    if (!file) {
      return;
    }
    clear();
    chosenName = file.name;
    const parsed = parseImportDocument(await file.text());
    if (!parsed.ok) {
      fileProblem = parsed.message;
      return;
    }
    batch = parsed.batch;
    count = parsed.count;
  }

  const refusal = $derived.by(() => {
    const err = load.error;
    return isMockulusError(err) && err.problems.length > 0 ? err : undefined;
  });

  /** One group per mapping the server complained about, in the file's own order. */
  interface ProblemGroup {
    /** The index in the submitted array, or `undefined` for a problem about the batch itself. */
    readonly index: number | undefined;
    readonly problems: { readonly problem: MockulusProblem; readonly within: string }[];
  }

  const groups = $derived.by((): ProblemGroup[] => {
    const err = refusal;
    if (!err) {
      return [];
    }
    // Grouped by scanning rather than by a keyed map, which keeps the file's own
    // order — the order the reader will walk their document in — and costs
    // nothing at the size an error list ever reaches.
    const collected: ProblemGroup[] = [];
    for (const problem of err.problems) {
      const parts = importPointerParts(problem.source?.pointer ?? '');
      let group = collected.find((candidate) => candidate.index === parts.index);
      if (!group) {
        group = { index: parts.index, problems: [] };
        collected.push(group);
      }
      group.problems.push({ problem, within: parts.within });
    }
    return collected;
  });

  /** How to name the mapping a group is about, from the document the user chose. */
  function describeMapping(index: number | undefined): string {
    if (index === undefined || !batch) {
      return 'The batch as a whole';
    }
    const mapping = batch.mappings?.[index];
    if (!mapping) {
      return `Mapping ${index + 1} of ${count}`;
    }
    const criterion = urlCriterionOf(mapping);
    return `Mapping ${index + 1} of ${count} — ${methodOf(mapping)} ${criterion?.value ?? 'any URL'}`;
  }
</script>

<section
  aria-label="Import mappings"
  class="rounded-lg border border-slate-300 bg-white px-5 py-4 dark:border-slate-700 dark:bg-slate-900"
>
  <h2 class="text-base font-semibold">Import mappings</h2>
  <p class="mt-1 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    A <code class="font-mono">&#123;"mappings": […]&#125;</code> document, which is what Export
    writes and what
    <code class="font-mono">POST /__admin/mappings/import</code> takes. The whole batch is checked before
    any of it is written.
  </p>

  <div class="mt-3 flex flex-wrap items-center gap-3">
    <!-- The input lives inside its label so that clicking the label opens the
         picker, the label text is the input's accessible name, and Tab still
         reaches a control that is visually hidden — with `focus-within` putting
         the focus ring somewhere a sighted keyboard user can see it. -->
    <label
      class="cursor-pointer rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-sky-600 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Choose a file…
      <input type="file" accept="application/json,.json" onchange={choose} class="sr-only" />
    </label>
    {#if chosenName}
      <span class="font-mono text-xs break-all text-slate-600 dark:text-slate-400"
        >{chosenName}</span
      >
      <button
        type="button"
        onclick={clear}
        class="text-xs font-medium text-slate-600 underline underline-offset-4 dark:text-slate-400"
      >
        Clear
      </button>
    {/if}
  </div>

  {#if fileProblem}
    <p
      role="status"
      class="mt-3 rounded-lg border border-rose-300 bg-rose-50 px-4 py-3 text-sm text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100"
    >
      {fileProblem}
    </p>
  {/if}

  {#if written !== undefined}
    <p
      role="status"
      class="mt-3 rounded-lg border border-emerald-300 bg-emerald-50 px-4 py-3 text-sm text-emerald-950 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-100"
    >
      Wrote {written}
      {written === 1 ? 'mapping' : 'mappings'}. The list below has been re-read from the server.
    </p>
  {/if}

  <!-- Hidden while a refusal is on screen: re-sending the same document would
       be refused the same way, and the instruction is to fix the file. -->
  {#if batch && !refusal}
    <div class="mt-3 rounded-lg border border-slate-200 px-4 py-3 dark:border-slate-800">
      <p class="text-sm">
        <strong>{count}</strong>
        {count === 1 ? 'mapping' : 'mappings'} in this file. Any whose
        <code class="font-mono">id</code> already exists here will be replaced, and the rest added.
      </p>
      <div class="mt-3 flex flex-wrap gap-2">
        <button
          type="button"
          disabled={load.pending}
          onclick={() => batch && load.run(batch)}
          class="rounded-md bg-sky-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-800 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {load.pending ? 'Writing…' : `Write ${count} ${count === 1 ? 'mapping' : 'mappings'}`}
        </button>
        <button
          type="button"
          disabled={load.pending}
          onclick={clear}
          class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:hover:bg-slate-800"
        >
          Cancel
        </button>
      </div>
    </div>
  {/if}

  {#if refusal}
    <div
      class="mt-3 rounded-lg border border-rose-300 bg-rose-50 px-5 py-4 text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100"
    >
      <h3 class="text-base font-semibold">Nothing was written</h3>
      <p class="mt-2 max-w-2xl text-sm">
        The import is atomic: the server compiles every mapping in the batch before it writes any of
        them, so this deployment holds exactly what it held before you pressed the button. Fix the
        mappings named below and choose the file again.
      </p>
      <ul aria-label="Rejected mappings" class="mt-3 space-y-3">
        {#each groups as group, index (index)}
          <li
            class="rounded border border-current/20 bg-white/50 px-3 py-2 text-sm dark:bg-black/20"
          >
            <p class="font-semibold">{describeMapping(group.index)}</p>
            <ul class="mt-1.5 space-y-1.5">
              {#each group.problems as entry, position (position)}
                <li>
                  <span class="font-mono text-xs opacity-70">code {entry.problem.code}</span>
                  {#if entry.within !== ''}
                    <code class="ml-2 font-mono text-xs font-semibold">{entry.within}</code>
                  {/if}
                  <span class="block">{entry.problem.detail ?? entry.problem.title ?? ''}</span>
                </li>
              {/each}
            </ul>
          </li>
        {/each}
      </ul>
    </div>
  {:else if load.error}
    <div class="mt-3">
      <ErrorState
        error={load.error}
        onretry={() => batch && load.run(batch)}
        onauthenticate={() => api.requestToken()}
      />
    </div>
  {/if}
</section>
