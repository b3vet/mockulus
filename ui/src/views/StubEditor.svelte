<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { isMockulusError, type MockulusClient, type StubMapping } from '@mockulus/admin-sdk';
  import { getApi } from '../lib/api.svelte';
  import { createAction } from '../lib/action.svelte';
  import AppLink from '../lib/components/AppLink.svelte';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import JsonEditor, { type JsonEditorController } from '../lib/components/JsonEditor.svelte';
  import ProblemList from '../lib/components/ProblemList.svelte';
  import { firstSyntaxError, resolveJsonPointer, type DocumentSpan } from '../lib/json-pointer';
  import { createResource } from '../lib/resource.svelte';
  import { toHref } from '../lib/router';
  import { getRouter } from '../lib/router.svelte';
  import {
    NEW_STUB_TEMPLATE,
    draftFor,
    editorModeOf,
    parseDraft,
    type EditorMode,
  } from '../lib/stub-draft';
  import { stubIdOf } from '../lib/stubs';

  /**
   * The stub editor: one mapping document, and the three ways of arriving at
   * one.
   *
   * `/stubs/new`, `/stubs/{id}/edit` and `/stubs/{id}/duplicate` are all this
   * view, because they differ in exactly two things — what the document starts
   * as, and which call saves it — and a second component would be this file with
   * two lines changed and its own bugs.
   *
   * Duplicating opens a draft rather than writing. A copy exists to become a
   * stub that differs somehow, and one that registered on the way to the editor
   * would put a second identical stub into the matching set for as long as it
   * took to edit it — on a shared deployment, into everyone's matching set.
   */
  const api = getApi();
  const router = getRouter();

  const mode = $derived(editorModeOf(router.path));
  const sourceId = $derived(router.params.id);

  /**
   * The stub the draft starts from, for the two modes that have one. Create
   * resolves to `null` without a request: there is nothing to read, and asking
   * the server anyway would be a call that can 401 on a page with nothing to
   * show for it.
   */
  async function loadSource(client: MockulusClient): Promise<StubMapping | null> {
    const id = sourceId;
    if (mode === 'create' || id === undefined || id === '') {
      return null;
    }
    // `getOrNull`, as the detail view does: an id that names nothing is an
    // ordinary outcome for a link followed after the stub was deleted, and
    // belongs in an empty state rather than beside genuine failures.
    return client.mappings.getOrNull(id);
  }

  const source = createResource(api, loadSource);

  /** The document, owned jointly with the editor: it is the editor's text. */
  let doc = $state('');
  let controller = $state<JsonEditorController | undefined>(undefined);

  /**
   * What went wrong on this side of the wire — the document is not JSON, or is
   * JSON that is not an object. Kept apart from the server's answer because the
   * two are different claims, and this one says the request was never made.
   */
  let localProblem = $state<string | undefined>(undefined);

  /**
   * Which route the draft in the editor was seeded for.
   *
   * The shell keeps one instance of this view alive across a move between the
   * three editor routes, since they name the same component. Without this,
   * following "duplicate" from an edit would leave the first document in the
   * editor — and Save would then write it under the second stub's rules.
   */
  let seededFor = $state<string | undefined>(undefined);
  let watchedPath = router.path;

  const save = createAction(
    api,
    async (client: MockulusClient, mapping: StubMapping): Promise<StubMapping> => {
      if (mode === 'edit' && sourceId !== undefined && sourceId !== '') {
        return client.mappings.update(sourceId, mapping);
      }
      return client.mappings.create(mapping);
    },
    (written) => {
      // Straight to the stub as the server now holds it. The answered document
      // carries the identity the server assigned, and landing on it is the
      // confirmation that the write took — better than a toast that has to be
      // believed.
      const id = stubIdOf(written);
      router.navigate(toHref(id === undefined ? '/stubs' : `/stubs/${encodeURIComponent(id)}`));
    },
  );

  $effect(() => {
    const path = router.path;
    if (path === watchedPath) {
      return;
    }
    watchedPath = path;
    seededFor = undefined;
    localProblem = undefined;
    save.reset();
    source.reload();
  });

  $effect(() => {
    const path = router.path;
    if (seededFor === path) {
      return;
    }
    if (mode === 'create') {
      doc = NEW_STUB_TEMPLATE;
      seededFor = path;
      return;
    }
    const loaded = source.data;
    if (loaded && mode !== undefined) {
      doc = draftFor(loaded, mode);
      seededFor = path;
    }
  });

  function attemptSave() {
    const parsed = parseDraft(doc);
    if (!parsed.ok) {
      localProblem = parsed.message;
      return;
    }
    localProblem = undefined;
    save.run(parsed.mapping);
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    attemptSave();
  }

  /**
   * The server's refusal, when it is the collect-all kind.
   *
   * Any 422 goes to the pointer-linked list. Everything else — a 401, a store
   * outage, a connection that never happened — goes to the shared error state,
   * which explains those better than a list of one entry would.
   */
  const refusal = $derived.by(() => {
    const err = save.error;
    return isMockulusError(err) && err.status === 422 ? err : undefined;
  });

  function jumpToSyntaxError() {
    const at = firstSyntaxError(doc);
    if (at) {
      controller?.reveal(at.from, at.to);
      return;
    }
    // A document that `JSON.parse` refuses but the grammar reads to the end —
    // trailing content after the value, most often. Focus is still the useful
    // move; pretending to a position would be worse than none.
    controller?.focus();
  }

  function reveal(span: DocumentSpan) {
    controller?.reveal(span.from, span.to);
  }

  const headings: Record<EditorMode, string> = {
    create: 'New stub',
    edit: 'Edit stub',
    duplicate: 'Duplicate stub',
  };
  const saveLabels: Record<EditorMode, string> = {
    create: 'Create stub',
    edit: 'Save changes',
    duplicate: 'Create the copy',
  };

  const backHref = $derived(
    mode === 'create' || sourceId === undefined || sourceId === ''
      ? toHref('/stubs')
      : toHref(`/stubs/${encodeURIComponent(sourceId)}`),
  );
  const backLabel = $derived(mode === 'create' ? '← All stubs' : '← Back to the stub');
  const waitingForSource = $derived(
    mode !== 'create' && source.data === undefined && source.error === undefined,
  );
</script>

<AppLink
  href={backHref}
  onnavigate={(href) => router.navigate(href)}
  class="text-sm font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
>
  {backLabel}
</AppLink>

{#if mode === undefined}
  <h1 class="mt-4 text-2xl font-semibold tracking-tight">Not an editor route</h1>
  <p class="mt-3 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    <code class="font-mono break-all">{router.path}</code> is not one of the editor's three paths.
  </p>
{:else}
  <h1 class="mt-4 text-2xl font-semibold tracking-tight">{headings[mode]}</h1>
  <p class="mt-3 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    {#if mode === 'create'}
      The document below is the whole stub, in the schema
      <code class="font-mono">POST /__admin/mappings</code> takes. Nothing is written until you press
      create.
    {:else if mode === 'duplicate'}
      A copy of this stub with its identity removed, so saving registers a second stub rather than
      replacing the first. Nothing has been written yet.
    {:else}
      The mapping as the server holds it. Saving replaces it whole and keeps its place in the
      matching order, so an edit does not promote a stub above its equal-priority peers.
    {/if}
  </p>

  {#if source.error}
    <div class="mt-6">
      <ErrorState
        error={source.error}
        onretry={() => source.reload()}
        onauthenticate={() => api.requestToken()}
      />
    </div>
  {:else if waitingForSource}
    <p role="status" class="mt-6 text-sm text-slate-600 dark:text-slate-400">Loading the stub…</p>
  {:else if mode !== 'create' && source.data === null}
    <h2 class="mt-6 text-base font-semibold">No stub with that id</h2>
    <p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
      The server answered that <code class="font-mono break-all">{sourceId ?? ''}</code> names nothing
      in this replica's snapshot, so there is nothing to open here.
    </p>
  {:else}
    <form onsubmit={submit} class="mt-8">
      <div class="flex flex-wrap items-baseline justify-between gap-2">
        <span class="text-sm font-medium">Mapping JSON</span>
        <span id="stub-json-help" class="text-xs text-slate-500 dark:text-slate-400">
          Tab moves out of the editor. The server checks the whole document and reports every
          problem at once.
        </span>
      </div>

      <div class="mt-2">
        <JsonEditor
          bind:value={doc}
          oncontroller={(next) => (controller = next)}
          label="Stub mapping JSON"
          describedBy="stub-json-help"
        />
      </div>

      {#if localProblem}
        <div
          role="status"
          class="mt-4 rounded-lg border border-rose-300 bg-rose-50 px-5 py-4 text-sm text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100"
        >
          <h2 class="text-base font-semibold">This document was not sent</h2>
          <p class="mt-2 max-w-2xl">
            It has to be a JSON object before the server can be asked anything about it, so nothing
            was written and nothing changed.
          </p>
          <p class="mt-2 font-mono text-xs break-all">{localProblem}</p>
          <button
            type="button"
            onclick={jumpToSyntaxError}
            class="mt-3 rounded-md border border-current/30 px-3 py-1.5 text-sm font-medium hover:bg-white/60 dark:hover:bg-black/30"
          >
            Go to where it stops parsing
          </button>
        </div>
      {/if}

      {#if refusal}
        <div
          class="mt-4 rounded-lg border border-rose-300 bg-rose-50 px-5 py-4 text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100"
        >
          <h2 class="text-base font-semibold">
            The server refused this mapping, and nothing was written
          </h2>
          <p class="mt-2 max-w-2xl text-sm">
            Every problem it found is below, not only the first: mockulus validates the whole
            document before answering, so one round trip reports all of them. Each entry that names
            a field will take you to it.
          </p>
          <div class="mt-3">
            <ProblemList
              problems={refusal.problems}
              locate={(pointer) => resolveJsonPointer(doc, pointer)}
              {reveal}
            />
          </div>
        </div>
      {:else if save.error}
        <div class="mt-4">
          <ErrorState
            error={save.error}
            onauthenticate={() => api.requestToken()}
            onretry={attemptSave}
          />
        </div>
      {/if}

      <div class="mt-6 flex flex-wrap items-center gap-3">
        <button
          type="submit"
          disabled={save.pending}
          class="rounded-md bg-sky-700 px-4 py-2 text-sm font-medium text-white hover:bg-sky-800 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {save.pending ? 'Saving…' : saveLabels[mode]}
        </button>
        <AppLink
          href={backHref}
          onnavigate={(href) => router.navigate(href)}
          class="rounded-md border border-slate-300 px-4 py-2 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
        >
          Discard and go back
        </AppLink>
      </div>
    </form>
  {/if}
{/if}
