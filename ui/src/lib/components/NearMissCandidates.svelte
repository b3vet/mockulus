<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import AppLink from './AppLink.svelte';
  import { getRouter } from '../router.svelte';
  import { toHref } from '../router';
  import { describeRequest } from '../journal-entries';
  import {
    candidateStub,
    differenceLabel,
    differencesOf,
    matchPercent,
    type NearMissGroup,
  } from '../near-miss-model';
  import { methodOf, stubIdOf, urlCriterionOf } from '../stubs';

  /**
   * The answer to "why did nothing match?", rendered as the data it is.
   *
   * The structured `differences` are a mockulus extension — WireMock reports its
   * diff as rendered prose on the serve event, and a caller who has to parse
   * prose to learn which header was wrong is being told less than the scorer
   * already knew. So each candidate gets a real table: the criterion, what the
   * stub asked for, what the request carried. That is the comparison the reader
   * came to make, and it is three columns rather than a paragraph.
   *
   * Every value in it — the URL, the header values, the body — came from
   * whoever drove the mock. Svelte escapes it, there is no `{@html}` here, and
   * the CSP is the second layer.
   */
  interface Props {
    groups: readonly NearMissGroup[];
    /**
     * What the request above each block is. The two modes mean different things
     * by it: the journal's is a request that arrived, the composer's is the
     * server's reading of a description, which is worth showing because it is
     * what the distances below were actually computed against.
     */
    requestCaption: string;
  }

  let { groups, requestCaption }: Props = $props();

  const router = getRouter();
</script>

<ul class="space-y-6">
  {#each groups as group, groupIndex (groupIndex)}
    <li
      class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900"
    >
      <div class="border-b border-slate-200 px-4 py-3 dark:border-slate-800">
        <p class="text-xs font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400">
          {requestCaption}
        </p>
        <p class="mt-1 font-mono text-sm break-all">{describeRequest(group.request)}</p>
      </div>

      {#if group.candidates.length === 0}
        <p class="px-4 py-3 text-sm text-slate-600 dark:text-slate-400">
          No stub was close enough to report.
        </p>
      {:else}
        <ol class="divide-y divide-slate-200 dark:divide-slate-800">
          {#each group.candidates as candidate, candidateIndex (candidateIndex)}
            {@const stub = candidateStub(candidate)}
            {@const id = stub ? stubIdOf(stub) : undefined}
            {@const differences = differencesOf(candidate)}
            <li class="px-4 py-4">
              <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <p class="font-mono text-sm break-all">
                  {#if stub}
                    <span
                      class="rounded bg-slate-200 px-1.5 py-0.5 text-xs font-semibold text-slate-700 dark:bg-slate-700 dark:text-slate-200"
                    >
                      {methodOf(stub)}
                    </span>
                    {urlCriterionOf(stub)?.value ?? 'any URL'}
                  {:else}
                    The supplied request pattern
                  {/if}
                </p>
                <p class="text-sm font-semibold">
                  {matchPercent(candidate.matchResult.distance)}% match
                  <span class="font-normal text-slate-500 dark:text-slate-400">
                    · distance {candidate.matchResult.distance.toFixed(2)}
                  </span>
                </p>
              </div>

              {#if stub?.name}
                <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">{stub.name}</p>
              {/if}

              {#if id}
                <p class="mt-1">
                  <AppLink
                    href={toHref(`/stubs/${encodeURIComponent(id)}`)}
                    onnavigate={(href) => router.navigate(href)}
                    class="text-sm font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
                  >
                    Open this stub
                  </AppLink>
                </p>
              {/if}

              {#if differences.length === 0}
                <p class="mt-3 text-sm text-slate-600 dark:text-slate-400">
                  Every criterion this candidate declares lined up. Where that is reported against a
                  stub rather than a pattern, the stub was outranked on priority or held back by its
                  scenario state rather than by its criteria.
                </p>
              {:else}
                <div class="mt-3 overflow-x-auto">
                  <table class="w-full text-left text-sm">
                    <caption class="sr-only">
                      What {stub?.name ?? 'this candidate'} asked for and what the request carried
                    </caption>
                    <thead
                      class="text-xs tracking-wide text-slate-500 uppercase dark:text-slate-400"
                    >
                      <tr>
                        <th scope="col" class="py-1 pr-4 font-semibold">Criterion</th>
                        <th scope="col" class="py-1 pr-4 font-semibold">Expected</th>
                        <th scope="col" class="py-1 font-semibold">Actual</th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100 dark:divide-slate-800">
                      {#each differences as difference, differenceIndex (differenceIndex)}
                        <tr>
                          <th
                            scope="row"
                            class="py-1.5 pr-4 align-top font-mono text-xs font-semibold whitespace-nowrap"
                          >
                            {differenceLabel(difference)}
                          </th>
                          <td class="py-1.5 pr-4 align-top font-mono text-xs break-all">
                            {difference.expected}
                          </td>
                          <td class="py-1.5 align-top font-mono text-xs break-all">
                            {difference.actual}
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
            </li>
          {/each}
        </ol>
      {/if}
    </li>
  {/each}
</ul>
