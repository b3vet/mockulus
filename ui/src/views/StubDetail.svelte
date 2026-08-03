<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
  import AppLink from '../lib/components/AppLink.svelte';
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import { createAction } from '../lib/action.svelte';
  import { getApi } from '../lib/api.svelte';
  import { createResource } from '../lib/resource.svelte';
  import { toHref } from '../lib/router';
  import { getRouter } from '../lib/router.svelte';
  import { methodOf, stubIdOf, urlCriterionOf } from '../lib/stubs';

  const api = getApi();
  const router = getRouter();

  async function load(client: MockulusClient): Promise<StubMapping | null> {
    const id = router.params.id;
    if (id === undefined || id === '') {
      return null;
    }
    // `getOrNull` rather than `get`: an id that names nothing is answered with a
    // bare 404 and is an ordinary outcome for a link followed after the stub was
    // deleted, so it belongs in the empty state below rather than in the error
    // state beside genuine failures.
    return client.mappings.getOrNull(id);
  }

  const resource = createResource(api, load);

  // The shell keeps one instance of this view alive across a move from one stub
  // to another, because the route component is the same one. Without this the
  // second stub would render the first one's document.
  let loadedId = router.params.id;
  $effect(() => {
    const id = router.params.id;
    if (id !== loadedId) {
      loadedId = id;
      resource.reload();
    }
  });

  const mapping = $derived(resource.data ?? undefined);
  const criterion = $derived(mapping ? urlCriterionOf(mapping) : undefined);
  const document = $derived(mapping ? JSON.stringify(mapping, null, 2) : '');

  /**
   * The id to act on, taken from the answered document rather than from the
   * path. The two agree in every ordinary case, and where they do not — a
   * document with no identity at all — the buttons should be absent rather than
   * pointed at an id the server never confirmed.
   */
  const id = $derived(mapping ? stubIdOf(mapping) : undefined);

  let confirmingDelete = $state(false);

  const remove = createAction(
    api,
    async (client: MockulusClient, stubId: string): Promise<void> => {
      await client.mappings.delete(stubId);
    },
    () => {
      confirmingDelete = false;
      // Back to the list, which re-reads the snapshot. Staying here would leave
      // the reader on a page describing a stub that no longer exists.
      router.navigate(toHref('/stubs'));
    },
  );
</script>

<AppLink
  href={toHref('/stubs')}
  onnavigate={(href) => router.navigate(href)}
  class="text-sm font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
>
  ← All stubs
</AppLink>

{#if resource.error}
  <div class="mt-6">
    <ErrorState
      error={resource.error}
      onretry={() => resource.reload()}
      onauthenticate={() => api.requestToken()}
    />
  </div>
{:else if resource.loading && resource.data === undefined}
  <p role="status" class="mt-6 text-sm text-slate-600 dark:text-slate-400">Loading stub…</p>
{:else if !mapping}
  <h1 class="mt-4 text-2xl font-semibold tracking-tight">No stub with that id</h1>
  <p class="mt-3 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    The server answered that <code class="font-mono break-all">{router.params.id ?? ''}</code> names nothing
    in this replica's snapshot. A stub deleted since the link was made, or an id from another deployment,
    both land here.
  </p>
{:else}
  <h1 class="mt-4 flex flex-wrap items-center gap-3 text-2xl font-semibold tracking-tight">
    <span
      class="rounded bg-slate-200 px-2 py-0.5 font-mono text-sm font-semibold text-slate-700 dark:bg-slate-700 dark:text-slate-200"
    >
      {methodOf(mapping)}
    </span>
    <span class="font-mono text-lg break-all">{criterion ? criterion.value : 'any URL'}</span>
  </h1>

  {#if mapping.name}
    <p class="mt-2 text-slate-600 dark:text-slate-400">{mapping.name}</p>
  {/if}

  {#if id}
    <div class="mt-5 flex flex-wrap items-center gap-2">
      <AppLink
        href={toHref(`/stubs/${encodeURIComponent(id)}/edit`)}
        onnavigate={(href) => router.navigate(href)}
        class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
      >
        Edit
      </AppLink>
      <AppLink
        href={toHref(`/stubs/${encodeURIComponent(id)}/duplicate`)}
        onnavigate={(href) => router.navigate(href)}
        class="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
      >
        Duplicate
      </AppLink>
      <ConfirmDialog
        bind:open={confirmingDelete}
        title="Delete this stub?"
        confirmLabel="Delete the stub"
        busy={remove.pending}
        onconfirm={() => id !== undefined && remove.run(id)}
        triggerClass="rounded-md border border-rose-300 px-3 py-1.5 text-sm font-medium text-rose-700 hover:bg-rose-50 dark:border-rose-900 dark:text-rose-300 dark:hover:bg-rose-950/40"
      >
        {#snippet trigger()}Delete{/snippet}
        This replica stops serving
        <code class="font-mono">{criterion ? criterion.value : 'any URL'}</code>
        from this stub before the call returns, and every other replica follows on its next snapshot.
        There is no undo — the document below is the only copy, so keep it if you may want it back.
      </ConfirmDialog>
    </div>
  {/if}

  {#if remove.error}
    <div class="mt-4">
      <ErrorState
        error={remove.error}
        onretry={() => id !== undefined && remove.run(id)}
        onauthenticate={() => api.requestToken()}
      />
    </div>
  {/if}

  <dl class="mt-8 grid gap-x-8 gap-y-3 text-sm sm:grid-cols-[max-content_1fr]">
    <dt class="font-semibold text-slate-500 dark:text-slate-400">Id</dt>
    <dd class="font-mono break-all">{mapping.id ?? mapping.uuid ?? '—'}</dd>

    <dt class="font-semibold text-slate-500 dark:text-slate-400">URL criterion</dt>
    <dd class="font-mono">{criterion ? criterion.kind : 'none — matches any URL'}</dd>

    <dt class="font-semibold text-slate-500 dark:text-slate-400">Priority</dt>
    <dd>{mapping.priority ?? 5}</dd>

    <dt class="font-semibold text-slate-500 dark:text-slate-400">Persistent</dt>
    <dd>{mapping.persistent ? 'yes' : 'no — swept by a mappings reset'}</dd>

    <dt class="font-semibold text-slate-500 dark:text-slate-400">Response status</dt>
    <dd>{mapping.response?.status ?? 200}</dd>

    {#if mapping.scenarioName}
      <dt class="font-semibold text-slate-500 dark:text-slate-400">Scenario</dt>
      <dd>
        {mapping.scenarioName}
        {#if mapping.requiredScenarioState}· serves in {mapping.requiredScenarioState}{/if}
        {#if mapping.newScenarioState}· then moves to {mapping.newScenarioState}{/if}
      </dd>
    {/if}
  </dl>

  <h2
    class="mt-10 text-sm font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400"
  >
    The document as stored
  </h2>
  <p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    What <code class="font-mono">GET /__admin/mappings/&#123;id&#125;</code> answers: the mapping as the
    server holds it, and the document Edit opens.
  </p>
  <pre
    class="mt-3 overflow-x-auto rounded-lg border border-slate-200 bg-white p-4 font-mono text-xs dark:border-slate-800 dark:bg-slate-900">{document}</pre>
{/if}
