<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import {
    isMockulusError,
    type MockulusClient,
    type ServeEvent,
    type ServeEventList,
  } from '@mockulus/admin-sdk';
  import AppLink from '../lib/components/AppLink.svelte';
  import ErrorState from '../lib/components/ErrorState.svelte';
  import { getApi } from '../lib/api.svelte';
  import {
    AUTO_REFRESH_INTERVAL_MS,
    DEFAULT_LIMIT,
    JOURNAL_TABS,
    LIMIT_CHOICES,
    SINCE_CHOICES,
    cookieRows,
    countsByOutcome,
    filterByTab,
    formatClockTime,
    headerRows,
    loggedAt,
    queryRows,
    sinceFrom,
    type JournalTab,
  } from '../lib/journal-entries';
  import { tablistTargetIndex } from '../lib/journal-tablist';
  import { offerDraft } from '../lib/near-miss-handoff';
  import { draftFromLoggedRequest } from '../lib/near-miss-model';
  import { createResource } from '../lib/resource.svelte';
  import { toHref } from '../lib/router';
  import { getRouter } from '../lib/router.svelte';

  /**
   * What this replica has served, newest first.
   *
   * The page's most common state is the empty one, and that is not a defect to
   * work around: the journal is **off by default** because mockulus makes every
   * expensive feature pay-per-use (SPEC §2), so a deployment nobody has
   * configured answers 500 with code 1010 here. That answer is rendered as the
   * configuration statement it is — what to set, where — rather than as a
   * failure, and none of the refresh machinery below runs against it.
   */

  const api = getApi();
  const router = getRouter();

  let limit = $state(DEFAULT_LIMIT);
  /** Minutes back from the moment of the read, or 0 for no bound. */
  let sinceMinutes = $state(0);
  let autoRefresh = $state(false);
  let tab = $state<JournalTab>('all');
  /** Which entry is expanded, by the id its listing carried. */
  let expandedId = $state<string | undefined>(undefined);

  async function load(client: MockulusClient): Promise<ServeEventList> {
    // The window is resolved here rather than held in state, because "the last
    // five minutes" means five minutes before *this* read. A bound computed once
    // when the control was touched would freeze, and an auto-refreshing page
    // would slowly stop showing the traffic it was opened to watch.
    return client.requests.list({ limit, since: sinceFrom(sinceMinutes, new Date()) });
  }

  const resource = createResource(api, load);

  const events = $derived<readonly ServeEvent[]>(resource.data?.requests ?? []);
  const counts = $derived(countsByOutcome(events));
  const visible = $derived(filterByTab(events, tab));
  const total = $derived(resource.data?.meta.total ?? 0);

  /**
   * The journal is off, which is a state of the deployment rather than a failed
   * call. Everything that would poll, window or filter is withdrawn while it
   * holds: there is nothing to window, and a timer re-asking a question whose
   * answer is a configuration file would be the clearest possible way to teach
   * an operator that a correctly configured mockulus looks broken.
   */
  const journalOff = $derived(isMockulusError(resource.error) && resource.error.isJournalDisabled);

  /** What the live region has to say. Rebuilt on each answer, which is what announces it. */
  let announcement = $state('');

  $effect(() => {
    const list = resource.data;
    if (list === undefined) {
      return;
    }
    const seen = countsByOutcome(list.requests);
    announcement =
      `Journal read at ${formatClockTime(new Date())}: ` +
      `${seen.all} ${seen.all === 1 ? 'entry' : 'entries'}, ${seen.unmatched} unmatched.`;
  });

  /**
   * Auto-refresh, and the reason it is an effect rather than a timer started by
   * the checkbox: an effect's teardown runs when the view is destroyed. A timer
   * that outlived its page would keep re-reading a journal nobody is looking at,
   * on every replica the tab was ever pointed at, for as long as the tab is
   * open.
   */
  $effect(() => {
    if (!autoRefresh || journalOff) {
      return;
    }
    const timer = setInterval(() => resource.reload(), AUTO_REFRESH_INTERVAL_MS);
    return () => clearInterval(timer);
  });

  function setLimit(value: string) {
    limit = Number(value);
    resource.reload();
  }

  function setSince(value: string) {
    sinceMinutes = Number(value);
    resource.reload();
  }

  function selectTab(next: JournalTab) {
    tab = next;
  }

  /**
   * Arrow keys move within the tab list, which is part of what makes these tabs
   * rather than three buttons wearing the word. Activation follows focus: the
   * tabs filter a list already in the browser, so there is no cost to moving
   * that would justify making the reader press Enter as well.
   */
  function onTabKeydown(event: KeyboardEvent, index: number) {
    const target = tablistTargetIndex(event.key, index, JOURNAL_TABS.length);
    if (target === undefined) {
      return;
    }
    const definition = JOURNAL_TABS[target];
    if (definition === undefined) {
      return;
    }
    event.preventDefault();
    selectTab(definition.id);
    const tabs = event.currentTarget as HTMLElement;
    const sibling = tabs.parentElement?.children[target];
    if (sibling instanceof HTMLElement) {
      sibling.focus();
    }
  }

  function toggle(id: string) {
    expandedId = expandedId === id ? undefined : id;
  }

  /**
   * Hands an unmatched entry to the near-miss debugger and goes there.
   *
   * A button rather than a link, because it does more than go to a page: it
   * carries the request across, and the request cannot travel in the URL. A
   * middle-click on a link would open the debugger with an empty form, which
   * looks like this action failing rather than like a different action.
   */
  function debugEntry(event: ServeEvent) {
    offerDraft(draftFromLoggedRequest(event.request));
    router.navigate(toHref('/near-misses'));
  }
</script>

{#snippet detail(event: ServeEvent)}
  {@const request = event.request}
  {@const at = loggedAt(request)}
  <div
    class="border-t border-slate-200 bg-slate-50 px-4 py-4 dark:border-slate-800 dark:bg-slate-950/40"
  >
    <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
      <dt class="font-semibold text-slate-500 dark:text-slate-400">Recorded</dt>
      <dd>{at ? at.toISOString() : 'not recorded on this entry'}</dd>

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Served</dt>
      <dd>
        {event.responseDefinition.status}
        {event.wasMatched ? '· from the stub below' : '· no stub matched'}
      </dd>

      {#if event.request.absoluteUrl}
        <dt class="font-semibold text-slate-500 dark:text-slate-400">Absolute URL</dt>
        <dd class="font-mono break-all">{event.request.absoluteUrl}</dd>
      {/if}

      {#if event.request.clientIp}
        <dt class="font-semibold text-slate-500 dark:text-slate-400">Client</dt>
        <dd class="font-mono">{event.request.clientIp}</dd>
      {/if}

      <dt class="font-semibold text-slate-500 dark:text-slate-400">Entry id</dt>
      <dd class="font-mono break-all">{event.id}</dd>
    </dl>

    {#if event.stubMapping}
      <p class="mt-4 text-sm">
        <span class="font-semibold">Served by</span>
        {event.stubMapping.name || 'an unnamed stub'} ·
        <AppLink
          href={toHref(`/stubs/${encodeURIComponent(event.stubMapping.id)}`)}
          onnavigate={(href) => router.navigate(href)}
          class="font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
        >
          Open this stub
        </AppLink>
      </p>
    {:else}
      <div class="mt-4">
        <p class="text-sm">Nothing in this replica's snapshot matched this request.</p>
        <button
          type="button"
          onclick={() => debugEntry(event)}
          class="mt-2 rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
        >
          Find out why nothing matched
        </button>
      </div>
    {/if}

    {#each [{ title: 'Headers', rows: headerRows(request) }, { title: 'Query parameters', rows: queryRows(request) }, { title: 'Cookies', rows: cookieRows(request) }] as section (section.title)}
      {#if section.rows.length > 0}
        <h4
          class="mt-4 text-xs font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400"
        >
          {section.title}
        </h4>
        <dl class="mt-1 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[max-content_1fr]">
          {#each section.rows as row (row.name)}
            <dt class="font-mono font-semibold break-all">{row.name}</dt>
            <dd class="font-mono break-all">{row.value}</dd>
          {/each}
        </dl>
      {/if}
    {/each}

    <h4
      class="mt-4 text-xs font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400"
    >
      Body
    </h4>
    {#if request.body === ''}
      <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">Empty.</p>
    {:else}
      <!-- Text from whoever drove the mock, rendered as text: Svelte escapes it
           and there is no `{@html}` on this page. -->
      <pre
        class="mt-1 max-h-72 overflow-auto rounded border border-slate-200 bg-white p-3 font-mono text-xs whitespace-pre-wrap dark:border-slate-800 dark:bg-slate-900">{request.body}</pre>
      {#if request.bodyTruncated}
        <p class="mt-1 text-xs text-amber-700 dark:text-amber-400">
          Cut at <code class="font-mono">journal_max_body</code>. The rest of this body was never
          recorded, so what is above is not the whole request.
        </p>
      {/if}
    {/if}
  </div>
{/snippet}

<h1 class="text-2xl font-semibold tracking-tight">Request journal</h1>
<p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
  What this replica recorded on its mock port, newest first. Admin calls are never journalled — only
  the traffic the mock served.
</p>

{#if journalOff}
  <div class="mt-6">
    <!-- Deliberately no retry: code 1010 is what a deployment answers until its
         configuration changes, and a "Try again" would promise that pressing it
         could help. -->
    <ErrorState error={resource.error} />
  </div>

  <section
    aria-label="What works without the journal"
    class="mt-4 rounded-lg border border-slate-200 bg-white px-5 py-4 dark:border-slate-800 dark:bg-slate-900"
  >
    <h2 class="text-base font-semibold">Debugging a stub does not need it</h2>
    <p class="mt-2 max-w-2xl text-sm text-slate-600 dark:text-slate-400">
      The near-miss debugger's compose mode scores the stubs in this replica's snapshot against a
      request you describe, and it reads no journal at all. It is the answer to "why is my stub not
      matching?" on exactly this deployment, unconfigured.
    </p>
    <p class="mt-3">
      <AppLink
        href={toHref('/near-misses')}
        onnavigate={(href) => router.navigate(href)}
        class="text-sm font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
      >
        Open the near-miss debugger
      </AppLink>
    </p>
  </section>
{:else}
  <div class="mt-6 flex flex-wrap items-end gap-4">
    <div>
      <label for="journal-limit" class="block text-sm font-medium">Entries to read</label>
      <select
        id="journal-limit"
        value={String(limit)}
        onchange={(event) => setLimit(event.currentTarget.value)}
        class="mt-1.5 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
      >
        {#each LIMIT_CHOICES as choice (choice)}
          <option value={String(choice)}>Newest {choice}</option>
        {/each}
      </select>
    </div>

    <div>
      <label for="journal-since" class="block text-sm font-medium">Recorded within</label>
      <select
        id="journal-since"
        value={String(sinceMinutes)}
        onchange={(event) => setSince(event.currentTarget.value)}
        class="mt-1.5 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
      >
        {#each SINCE_CHOICES as choice (choice.minutes)}
          <option value={String(choice.minutes)}>{choice.label}</option>
        {/each}
      </select>
    </div>

    <div class="flex items-center gap-2 py-2">
      <input
        id="journal-auto-refresh"
        type="checkbox"
        bind:checked={autoRefresh}
        class="size-4 rounded border-slate-400 dark:border-slate-600"
      />
      <label for="journal-auto-refresh" class="text-sm font-medium">
        Re-read every {AUTO_REFRESH_INTERVAL_MS / 1000} seconds
      </label>
    </div>

    <button
      type="button"
      onclick={() => resource.reload()}
      class="rounded-md border border-slate-300 px-3 py-2 text-sm font-medium hover:bg-slate-100 dark:border-slate-700 dark:hover:bg-slate-800"
    >
      Refresh
    </button>
  </div>

  <!-- Present from the first render so that its text changing is an
       announcement rather than a region appearing. -->
  <p role="status" class="mt-3 text-sm text-slate-600 dark:text-slate-400">{announcement}</p>

  {#if resource.error}
    <div class="mt-4">
      <ErrorState
        error={resource.error}
        onretry={() => resource.reload()}
        onauthenticate={() => api.requestToken()}
      />
    </div>
  {:else if resource.loading && resource.data === undefined}
    <p class="mt-4 text-sm text-slate-600 dark:text-slate-400">Reading the journal…</p>
  {:else}
    <div role="tablist" aria-label="Journal entries by outcome" class="mt-4 flex gap-1">
      {#each JOURNAL_TABS as definition, index (definition.id)}
        {@const selected = tab === definition.id}
        <button
          type="button"
          role="tab"
          id={`journal-tab-${definition.id}`}
          aria-selected={selected}
          aria-controls="journal-entries"
          tabindex={selected ? 0 : -1}
          onclick={() => selectTab(definition.id)}
          onkeydown={(event) => onTabKeydown(event, index)}
          class="rounded-t-md border-b-2 px-3 py-2 text-sm font-medium {selected
            ? 'border-sky-600 text-sky-700 dark:text-sky-400'
            : 'border-transparent text-slate-600 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100'}"
        >
          {definition.label}
          <span class="ml-1 text-xs text-slate-500 dark:text-slate-400">
            {counts[definition.id]}
          </span>
        </button>
      {/each}
    </div>

    <div id="journal-entries" role="tabpanel" aria-labelledby={`journal-tab-${tab}`} tabindex="-1">
      {#if visible.length === 0}
        <div
          class="mt-4 rounded-lg border border-slate-300 bg-white px-5 py-8 text-center dark:border-slate-700 dark:bg-slate-900"
        >
          {#if events.length === 0}
            <h2 class="text-base font-semibold">The journal is on and holds nothing here</h2>
            <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
              Recording is enabled, and no request in this window was recorded. Drive traffic at the
              mock port — the admin port's own calls are never journalled — and widen the window
              above if the traffic is older than it.
            </p>
          {:else}
            <h2 class="text-base font-semibold">Nothing in this window was {tab}</h2>
            <p class="mx-auto mt-2 max-w-md text-sm text-slate-600 dark:text-slate-400">
              {counts.all} recorded {counts.all === 1 ? 'request' : 'requests'} were read, and none of
              them is on this tab.
            </p>
          {/if}
        </div>
      {:else}
        <ul
          aria-label="Journal entries"
          class="mt-4 divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900"
        >
          {#each visible as event (event.id)}
            {@const at = loggedAt(event.request)}
            <li>
              <button
                type="button"
                aria-expanded={expandedId === event.id}
                onclick={() => toggle(event.id)}
                class="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left hover:bg-slate-50 dark:hover:bg-slate-800"
              >
                <span
                  class="rounded px-1.5 py-0.5 font-mono text-xs font-semibold {event.wasMatched
                    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-200'
                    : 'bg-amber-100 text-amber-900 dark:bg-amber-900/50 dark:text-amber-100'}"
                >
                  {event.request.method}
                </span>
                <span class="font-mono text-sm break-all">{event.request.url}</span>
                <span class="text-xs text-slate-500 dark:text-slate-400">
                  {event.responseDefinition.status}
                  {event.wasMatched ? '· matched' : '· unmatched'}
                  {#if at}· {formatClockTime(at)}{/if}
                </span>
              </button>
              {#if expandedId === event.id}
                {@render detail(event)}
              {/if}
            </li>
          {/each}
        </ul>

        <p class="mt-3 text-sm text-slate-600 dark:text-slate-400">
          Showing {visible.length.toLocaleString()} of the {counts.all.toLocaleString()} newest
          {counts.all === 1 ? 'entry' : 'entries'} read. The journal holds {total.toLocaleString()} in
          this window.
          {#if resource.loading}· re-reading…{/if}
        </p>
        {#if counts.all < total}
          <p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
            The journal has no offset — <code class="font-mono">limit</code> takes the newest entries
            and nothing pages past them — so read more of them above rather than looking for a next page.
          </p>
        {/if}
      {/if}
    </div>
  {/if}
{/if}
