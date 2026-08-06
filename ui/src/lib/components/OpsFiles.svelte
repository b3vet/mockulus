<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
  import ErrorState from './ErrorState.svelte';
  import OpsFileRow from './OpsFileRow.svelte';
  import { createAction } from '../action.svelte';
  import { getApi } from '../api.svelte';
  import { danglingReferences, formatBytes, referencesByFile } from '../ops-files';
  import { downloadBytes } from '../download';
  import type { Resource } from '../resource.svelte';

  /**
   * The response-body file store, which backs `bodyFileName`.
   *
   * A body is uploaded once and referenced by many stubs, and the two are
   * independent in both directions: registering a stub before its file exists is
   * legal and resolves when the file arrives, and deleting a file leaves the
   * stubs referencing it serving code 1022 rather than failing to load. Both
   * halves of that are surfaced here — the back-links per file, and the
   * references that name a file the store does not hold — because neither shows
   * up anywhere else in the UI and both are silent until traffic arrives.
   *
   * Names are arbitrary text from whoever drove the mock and may contain
   * slashes, since the store holds names rather than paths. They are rendered as
   * text and never as markup.
   */
  interface Props {
    files: Resource<string[]>;
    /** The mappings, read once by the page, for the `bodyFileName` back-links. */
    mappings: Resource<readonly StubMapping[]>;
  }

  let { files, mappings }: Props = $props();

  const api = getApi();

  const stored = $derived(files.data ?? []);
  const index = $derived(referencesByFile(mappings.data ?? []));
  const dangling = $derived(danglingReferences(mappings.data ?? [], stored));

  /** The chosen local file, and the name it will be stored under — which need not be the same. */
  let chosen = $state<File | undefined>(undefined);
  let nameDraft = $state('');
  let uploaded = $state<string | undefined>(undefined);

  function choose(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    // Reset the control so choosing the same file twice — after editing it on
    // disk — still fires a change event.
    input.value = '';
    if (!file) {
      return;
    }
    chosen = file;
    uploaded = undefined;
    upload.reset();
    if (nameDraft.trim() === '') {
      nameDraft = file.name;
    }
  }

  const upload = createAction(
    api,
    async (client: MockulusClient, name: string, file: File): Promise<string> => {
      // The bytes go up verbatim. `file.text()` would decode them as UTF-8 and
      // re-encode, which silently corrupts every body that is not valid UTF-8 —
      // and a response-body file is as likely to be a PNG or a protobuf as it
      // is to be JSON.
      await client.files.put(name, await file.arrayBuffer(), {
        contentType: file.type === '' ? 'application/octet-stream' : file.type,
      });
      return name;
    },
    (name) => {
      uploaded = name;
      chosen = undefined;
      nameDraft = '';
      files.reload();
      // The mappings too: a stub whose dangling reference this upload just
      // satisfied should stop being listed as dangling without a page reload.
      mappings.reload();
    },
  );

  const download = createAction(
    api,
    async (client: MockulusClient, name: string): Promise<void> => {
      const bytes = await client.files.get(name);
      downloadBytes(name, bytes);
    },
  );

  const remove = createAction(
    api,
    async (client: MockulusClient, name: string): Promise<void> => {
      await client.files.delete(name);
    },
    () => {
      files.reload();
      mappings.reload();
    },
  );

  const busy = $derived(upload.pending || download.pending || remove.pending);
  const problem = $derived(upload.error ?? download.error ?? remove.error);

  function submit(event: SubmitEvent) {
    event.preventDefault();
    const name = nameDraft.trim();
    if (name === '' || !chosen) {
      return;
    }
    uploaded = undefined;
    upload.run(name, chosen);
  }
</script>

<section aria-labelledby="ops-files-heading">
  <h2 id="ops-files-heading" class="text-lg font-semibold tracking-tight">Response-body files</h2>
  <p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
    What <code class="font-mono">bodyFileName</code> resolves against. A body uploaded here can be referenced
    by any number of stubs, and the reference is checked when the snapshot is built rather than when the
    stub is registered — so a stub may name a file that does not exist yet, and a file may exist that
    nothing references.
  </p>

  <form onsubmit={submit} class="mt-4 max-w-xl">
    <div class="flex flex-wrap items-center gap-3">
      <!-- The input lives inside its label so that clicking the label opens the
           picker, the label text is the input's accessible name, and Tab still
           reaches a control that is visually hidden. -->
      <label
        class="cursor-pointer rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-sky-600 dark:border-slate-700 dark:hover:bg-slate-800"
      >
        Choose a file…
        <input type="file" onchange={choose} class="sr-only" />
      </label>
      {#if chosen}
        <span class="text-xs text-slate-600 dark:text-slate-400">
          {formatBytes(chosen.size)}
        </span>
      {/if}
    </div>

    <div class="mt-3">
      <label for="file-name" class="block text-sm font-medium">Store it as</label>
      <input
        id="file-name"
        type="text"
        bind:value={nameDraft}
        aria-describedby="file-name-help"
        placeholder="fixtures/order.json"
        class="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
      />
      <p id="file-name-help" class="mt-1 text-xs text-slate-500 dark:text-slate-400">
        The name a stub's <code class="font-mono">bodyFileName</code> will use. Slashes are part of the
        name, not a directory path. A name the store cannot hold is refused by the server, and the refusal
        appears below.
      </p>
    </div>

    <div class="mt-3 flex flex-wrap items-center gap-2">
      <button
        type="submit"
        disabled={busy || !chosen || nameDraft.trim() === ''}
        class="rounded-md bg-sky-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-800 disabled:cursor-not-allowed disabled:opacity-50"
      >
        {upload.pending ? 'Uploading…' : 'Upload'}
      </button>
      <span class="text-xs text-slate-500 dark:text-slate-400">
        An existing name is replaced, and the replica that takes the upload serves the new bytes on
        the very next request.
      </span>
    </div>
  </form>

  {#if uploaded}
    <p
      role="status"
      class="mt-3 rounded-lg border border-emerald-300 bg-emerald-50 px-4 py-3 text-sm text-emerald-950 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-100"
    >
      Stored as <code class="font-mono break-all">{uploaded}</code>.
    </p>
  {/if}

  {#if problem}
    <div class="mt-3">
      <ErrorState error={problem} onauthenticate={() => api.requestToken()} />
    </div>
  {/if}

  <div class="mt-6">
    {#if files.error}
      <ErrorState
        error={files.error}
        onretry={() => files.reload()}
        onauthenticate={() => api.requestToken()}
      />
    {:else if files.data === undefined}
      <p role="status" class="text-sm text-slate-600 dark:text-slate-400">Reading the files…</p>
    {:else if stored.length === 0}
      <p class="text-sm text-slate-600 dark:text-slate-400">
        The file store is empty. Nothing is wrong with that — a deployment whose stubs all carry
        inline bodies never needs one.
      </p>
    {:else}
      <ul
        aria-label="Stored files"
        class="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900"
      >
        {#each stored as name (name)}
          <OpsFileRow
            {name}
            references={index.get(name) ?? []}
            {busy}
            ondownload={(target) => download.run(target)}
            ondelete={(target) => remove.run(target)}
          />
        {/each}
      </ul>
    {/if}

    {#if dangling.length > 0}
      <div
        class="mt-4 rounded-lg border border-amber-300 bg-amber-50 px-5 py-4 text-sm text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-100"
      >
        <h3 class="text-base font-semibold">
          {dangling.length}
          {dangling.length === 1 ? 'reference names' : 'references name'} a file the store does not hold
        </h3>
        <p class="mt-2 max-w-2xl">
          Every stub pointing at one of these serves 500 with code 1022 until the file arrives. That
          is by design — one missing file does not take a deployment's stubs down with it — but it
          means the breakage is silent until a request hits the stub.
        </p>
        <ul class="mt-3 space-y-1">
          {#each dangling as name (name)}
            <li>
              <button
                type="button"
                onclick={() => (nameDraft = name)}
                class="font-mono text-xs break-all underline underline-offset-4"
              >
                {name}
              </button>
            </li>
          {/each}
        </ul>
        <p class="mt-2 text-xs">Choosing one puts it in the name box above.</p>
      </div>
    {/if}

    {#if mappings.error}
      <p class="mt-3 text-xs text-slate-500 dark:text-slate-400">
        The mappings could not be read, so the back-links above are missing rather than empty: a
        file listed as referenced by nothing may still be referenced.
      </p>
    {/if}
  </div>
</section>
