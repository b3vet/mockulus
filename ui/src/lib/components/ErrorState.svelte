<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { isMockulusError, type MockulusProblem } from '@mockulus/admin-sdk';

  /**
   * What the UI shows instead of the thing that did not load.
   *
   * Errors on this surface are states of the deployment, not incidents, and the
   * SOW asks for them to be rendered as such rather than thrown at a toast that
   * disappears before it has been read. Two get their own explanation:
   *
   * - **1010, the journal is disabled.** This is the *default* configuration
   *   and by far the most common answer a journal-backed call gives, so it is
   *   written as an instruction rather than a failure. A red box here would
   *   teach operators that a correctly configured mockulus looks broken.
   * - **1020, the store is unavailable.** Degraded rather than down: the
   *   replica keeps serving from its compiled snapshot, so the honest message
   *   says what still works and what does not.
   *
   * A 422 gets the whole `errors[]` list with its JSON pointers, because
   * mockulus collects every problem before answering — reporting one of three
   * would waste the round trip the collect-all envelope exists to save.
   */
  interface Props {
    error: unknown;
    /** Re-runs whatever failed. Shown as "Try again" where it is supplied. */
    onretry?: () => void;
    /** Opens the token sheet. Shown only on a 401. */
    onauthenticate?: () => void;
  }

  let { error, onretry, onauthenticate }: Props = $props();

  type Kind = 'journal' | 'store' | 'unauthorized' | 'refused' | 'unreachable' | 'unknown';

  const mockulusError = $derived(isMockulusError(error) ? error : undefined);

  const kind = $derived.by((): Kind => {
    const err = mockulusError;
    if (!err) {
      // Not an answer from the server at all. `fetch` rejects with a TypeError
      // when the connection never happened, which on this UI usually means the
      // deployment behind the dev proxy is not running.
      return error instanceof TypeError ? 'unreachable' : 'unknown';
    }
    if (err.isJournalDisabled) return 'journal';
    if (err.isStoreUnavailable) return 'store';
    if (err.isUnauthorized) return 'unauthorized';
    if (err.status === 422) return 'refused';
    return 'unknown';
  });

  const problems = $derived<readonly MockulusProblem[]>(mockulusError?.problems ?? []);

  const titles: Record<Kind, string> = {
    journal: 'The request journal is off',
    store: 'The stub store is unavailable',
    unauthorized: 'This deployment needs an admin token',
    refused: 'The server refused the request',
    unreachable: 'Could not reach the admin API',
    unknown: 'Something went wrong',
  };

  // The journal case is deliberately not styled as a failure: it is what a
  // default deployment answers, and colouring it red would be the UI disagreeing
  // with the configuration the operator chose on purpose.
  const tones: Record<Kind, string> = {
    journal:
      'border-sky-300 bg-sky-50 text-sky-950 dark:border-sky-900/60 dark:bg-sky-950/40 dark:text-sky-100',
    store:
      'border-amber-300 bg-amber-50 text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-100',
    unauthorized:
      'border-amber-300 bg-amber-50 text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-100',
    refused:
      'border-rose-300 bg-rose-50 text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100',
    unreachable:
      'border-rose-300 bg-rose-50 text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100',
    unknown:
      'border-rose-300 bg-rose-50 text-rose-950 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-100',
  };

  const message = $derived(
    mockulusError?.message ?? (error instanceof Error ? error.message : String(error)),
  );
</script>

<section role="status" class="rounded-lg border px-5 py-4 {tones[kind]}">
  <h2 class="text-base font-semibold">{titles[kind]}</h2>

  {#if kind === 'journal'}
    <p class="mt-2 max-w-2xl text-sm">
      Nothing is broken here. mockulus keeps the request journal off unless it is asked for —
      recording every request costs memory and hot-path time, and the project charges for a feature
      only where it is wanted — so a deployment that has never been configured answers this.
    </p>
    <p class="mt-2 max-w-2xl text-sm">
      To record requests, set <code class="font-mono">journal_enabled: true</code> in the
      configuration file or <code class="font-mono">MOCKULUS_JOURNAL_ENABLED=true</code> in the environment,
      and restart the deployment.
    </p>
  {:else if kind === 'store'}
    <p class="mt-2 max-w-2xl text-sm">
      This replica is still serving mock traffic from the snapshot it has already compiled, and the
      stubs listed here are the ones it will match against. What it cannot do until the store is
      back is make an admin write durable.
    </p>
    <p class="mt-2 max-w-2xl text-sm">
      Check the store the deployment resolved to — <code class="font-mono">GET /__admin/health</code
      > names the driver — then try again. This is the one failure on this surface that retrying is expected
      to fix.
    </p>
  {:else if kind === 'unauthorized'}
    <p class="mt-2 max-w-2xl text-sm">
      <code class="font-mono">admin_auth_token</code> is configured on this deployment, so the admin
      API will not answer without it. The UI keeps the token for this browser tab only and sends it
      as an <code class="font-mono">Authorization</code> header; it is never written to a URL, a cookie
      or storage that outlives the tab.
    </p>
  {:else if kind === 'refused'}
    <p class="mt-2 max-w-2xl text-sm">
      Every problem the server found is below, not just the first — mockulus validates the whole
      document before answering. Each pointer names the element to fix.
    </p>
  {:else if kind === 'unreachable'}
    <p class="mt-2 max-w-2xl text-sm">
      The request never got an answer. In development this usually means the mockulus the dev server
      proxies to is not running.
    </p>
    <p class="mt-2 font-mono text-xs break-all">{message}</p>
  {:else}
    <p class="mt-2 max-w-2xl font-mono text-xs break-all">{message}</p>
  {/if}

  {#if problems.length > 0 && kind !== 'journal' && kind !== 'store'}
    <ul class="mt-3 space-y-2 text-sm">
      {#each problems as problem, index (index)}
        <li class="rounded border border-current/20 bg-white/50 px-3 py-2 dark:bg-black/20">
          <span class="font-mono text-xs opacity-70">code {problem.code}</span>
          {#if problem.source?.pointer}
            <code class="ml-2 font-mono text-xs font-semibold">{problem.source.pointer}</code>
          {/if}
          <p class="mt-1">{problem.detail ?? problem.title ?? 'No detail given.'}</p>
        </li>
      {/each}
    </ul>
  {/if}

  {#if onretry || (onauthenticate && kind === 'unauthorized')}
    <div class="mt-4 flex flex-wrap gap-2">
      {#if onauthenticate && kind === 'unauthorized'}
        <button
          type="button"
          onclick={onauthenticate}
          class="rounded-md border border-current/30 px-3 py-1.5 text-sm font-medium hover:bg-white/60 dark:hover:bg-black/30"
        >
          Enter token
        </button>
      {/if}
      {#if onretry}
        <button
          type="button"
          onclick={onretry}
          class="rounded-md border border-current/30 px-3 py-1.5 text-sm font-medium hover:bg-white/60 dark:hover:bg-black/30"
        >
          Try again
        </button>
      {/if}
    </div>
  {/if}
</section>
