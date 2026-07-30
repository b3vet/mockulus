<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  const planned = [
    'Stubs — browse, filter, edit, import and export mappings',
    'Journal — the request log, matched and unmatched',
    'Near-miss debugger — why a request did not match',
    'Scenarios — current state and one-click transitions',
    'Ops — health, files, settings, danger zone',
  ];
</script>

<h1 class="text-2xl font-semibold tracking-tight">Overview</h1>

<p class="mt-3 max-w-2xl text-slate-600 dark:text-slate-400">
  This shell is served from the mockulus binary and routed entirely in the browser. It is what the
  first stage of the admin UI is for: proving the toolchain and the serving contract end to end.
</p>

<div
  class="mt-8 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-200"
>
  <strong class="font-semibold">No data yet.</strong>
  Nothing on this page has talked to the admin API. The typed client it will call — the admin SDK — does
  not exist yet, so the UI stays honest about having no data rather than inventing a placeholder for it.
</div>

<h2 class="mt-10 text-sm font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400">
  Coming in later stages
</h2>
<ul class="mt-3 space-y-2">
  {#each planned as item (item)}
    <li class="flex gap-3 text-slate-700 dark:text-slate-300">
      <span aria-hidden="true" class="text-slate-400 dark:text-slate-600">—</span>
      <span>{item}</span>
    </li>
  {/each}
</ul>
