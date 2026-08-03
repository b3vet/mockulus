<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { DescribedRequest, MockulusClient, NearMissList } from '@mockulus/admin-sdk';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import NearMissCandidates from '../lib/components/NearMissCandidates.svelte';
  import NearMissFromJournal from '../lib/components/NearMissFromJournal.svelte';
  import TabList from '../lib/components/TabList.svelte';
  import { createAction } from '../lib/action.svelte';
  import { getApi } from '../lib/api.svelte';
  import { takeDraft } from '../lib/near-miss-handoff';
  import { emptyDraft, groupByRequest, toDescribedRequest } from '../lib/near-miss-model';
  import type { TabDefinition } from '../lib/tablist';

  /**
   * Why nothing matched, in the two shapes that question comes in.
   *
   * **Compose a request** hands the server a request somebody describes and gets
   * back the stubs that came closest. It touches no journal — `POST
   * /__admin/near-misses/request` has no journal-disabled answer in the contract
   * at all — which is the whole point of it: the deployment anyone debugging a
   * stub that will not match is standing in front of is the default one, with
   * recording off. That is why it is the mode this page opens on.
   *
   * **From the journal** takes the requests that actually arrived and matched
   * nothing. It needs `journal_enabled`, and says so plainly when it does not
   * have it rather than looking broken.
   */

  const api = getApi();

  type Mode = 'compose' | 'journal';

  const MODES: readonly TabDefinition<Mode>[] = [
    { id: 'compose', label: 'Compose a request' },
    { id: 'journal', label: 'From the journal' },
  ];

  /**
   * A request the journal handed over, taken once. `undefined` on an ordinary
   * visit, which is the blank form.
   */
  const handed = takeDraft();

  let mode = $state<Mode>('compose');
  let draft = $state(handed ?? emptyDraft());
  let carried = $state(handed !== undefined);
  /** Why the form cannot be sent as typed. Set instead of a call, never with one. */
  let formProblem = $state<string | undefined>(undefined);
  let result = $state.raw<NearMissList | undefined>(undefined);

  /**
   * Mounted from the first time its tab is opened and left mounted afterwards,
   * so returning to it shows what it already found rather than spending the read
   * again. Until then the journal is never asked anything.
   */
  let journalOpened = $state(false);

  const score = createAction(
    api,
    async (client: MockulusClient, request: DescribedRequest): Promise<NearMissList> =>
      client.nearMisses.forRequest(request),
    (list) => {
      result = list;
    },
  );

  const groups = $derived(groupByRequest(result?.nearMisses ?? []));

  function selectMode(next: Mode) {
    mode = next;
    if (next === 'journal') {
      journalOpened = true;
    }
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    carried = false;
    const parsed = toDescribedRequest(draft);
    if (!parsed.ok) {
      // Refused here rather than sent: a header line without a colon is a typing
      // mistake with an obvious fix, and a round trip would answer it with a
      // scored request the reader did not describe.
      formProblem = parsed.message;
      result = undefined;
      return;
    }
    formProblem = undefined;
    score.run(parsed.request);
  }

  function reset() {
    draft = emptyDraft();
    formProblem = undefined;
    result = undefined;
    carried = false;
    score.reset();
  }
</script>

<h1 class="text-2xl font-semibold tracking-tight">Near-miss debugger</h1>
<p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
  Which stubs came closest, and on which criteria they differed. The ranking is mockulus's own
  rather than WireMock's — near-miss output is a debugging aid, and no matching decision depends on
  the order.
</p>

<!-- Each mode gets its own panel, kept in the document and hidden when it is not
     the current one, because the journal mode holds a form and a read that
     should survive a look at the other tab. -->
<TabList
  label="Near-miss modes"
  tabs={MODES}
  selected={mode}
  onselect={selectMode}
  tabId={(id) => `near-miss-tab-${id}`}
  panelId={(id) => `near-miss-panel-${id}`}
  class="mt-6"
/>

<div
  id="near-miss-panel-compose"
  role="tabpanel"
  aria-labelledby="near-miss-tab-compose"
  tabindex="-1"
  hidden={mode !== 'compose'}
>
  <p class="mt-4 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    Describe a request and the server ranks the stubs in its snapshot against it. Nothing is served
    and nothing is recorded — this is a question about the stubs, and it works on a deployment that
    never turned the journal on.
  </p>

  {#if carried}
    <p
      class="mt-3 rounded-lg border border-sky-300 bg-sky-50 px-4 py-3 text-sm text-sky-950 dark:border-sky-900/60 dark:bg-sky-950/40 dark:text-sky-100"
    >
      Filled in from the journal entry you came from. A header that arrived more than once was
      carried across as its first value, since a described request takes one value per name.
    </p>
  {/if}

  <form aria-label="Describe a request" onsubmit={submit} class="mt-4 grid gap-4">
    <div class="grid gap-4 sm:grid-cols-[max-content_1fr]">
      <div>
        <label for="near-miss-method" class="block text-sm font-medium">Method</label>
        <input
          id="near-miss-method"
          type="text"
          bind:value={draft.method}
          class="mt-1.5 w-32 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
        />
      </div>
      <div>
        <label for="near-miss-url" class="block text-sm font-medium">URL</label>
        <input
          id="near-miss-url"
          type="text"
          bind:value={draft.url}
          aria-describedby="near-miss-url-help"
          class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
        />
        <p id="near-miss-url-help" class="mt-1 text-xs text-slate-500 dark:text-slate-400">
          The path and query as a request would carry it, such as
          <code class="font-mono">/api/orders?dryRun=true</code>. The query string is read out of
          it.
        </p>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2">
      <div>
        <label for="near-miss-headers" class="block text-sm font-medium">Headers</label>
        <textarea
          id="near-miss-headers"
          rows="4"
          bind:value={draft.headers}
          placeholder="Content-Type: application/json"
          class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
        ></textarea>
      </div>
      <div>
        <label for="near-miss-cookies" class="block text-sm font-medium">Cookies</label>
        <textarea
          id="near-miss-cookies"
          rows="4"
          bind:value={draft.cookies}
          placeholder="session: abc123"
          class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
        ></textarea>
      </div>
    </div>

    <div>
      <label for="near-miss-body" class="block text-sm font-medium">Body</label>
      <textarea
        id="near-miss-body"
        rows="5"
        bind:value={draft.body}
        class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
      ></textarea>
    </div>

    <div class="flex flex-wrap gap-2">
      <button
        type="submit"
        disabled={score.pending}
        class="rounded-md bg-sky-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-800 disabled:cursor-not-allowed disabled:opacity-50"
      >
        {score.pending ? 'Scoring…' : 'Find the closest stubs'}
      </button>
      <button
        type="button"
        onclick={reset}
        class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
      >
        Clear
      </button>
    </div>
  </form>

  {#if formProblem}
    <p
      role="status"
      class="mt-4 rounded-lg border border-rose-300 bg-rose-50 px-4 py-3 text-sm text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100"
    >
      {formProblem}
    </p>
  {/if}

  {#if score.error}
    <div class="mt-4">
      <ErrorState error={score.error} onauthenticate={() => api.requestToken()} />
    </div>
  {:else if result !== undefined}
    {#if groups.length === 0}
      <div
        class="mt-4 rounded-lg border border-slate-300 bg-white px-5 py-8 text-center dark:border-slate-700 dark:bg-slate-900"
      >
        <h2 class="text-base font-semibold">No stub came close enough to report</h2>
        <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
          This replica's snapshot holds nothing that resembles the request. An empty deployment
          answers this, and so does one whose stubs are about a different service entirely.
        </p>
      </div>
    {:else}
      <h2
        class="mt-6 text-sm font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400"
      >
        Closest first
      </h2>
      <div class="mt-3">
        <NearMissCandidates {groups} requestCaption="As the server read your description" />
      </div>
    {/if}
  {/if}
</div>

<div
  id="near-miss-panel-journal"
  role="tabpanel"
  aria-labelledby="near-miss-tab-journal"
  tabindex="-1"
  hidden={mode !== 'journal'}
>
  {#if journalOpened}
    <NearMissFromJournal oncompose={() => selectMode('compose')} />
  {/if}
</div>
