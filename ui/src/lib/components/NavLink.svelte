<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import AppLink from './AppLink.svelte';

  interface Props {
    /** The real URL, so the browser's own navigation affordances still work. */
    href: string;
    label: string;
    active?: boolean;
    onnavigate?: (href: string) => void;
  }

  let { href, label, active = false, onnavigate }: Props = $props();
</script>

<!-- The primary nav's styling over the shared routing anchor. How a click is
     decided lives in AppLink, so a row in the stub list and a tab in the header
     cannot drift apart on modifier or middle-click handling. -->
<AppLink
  {href}
  {onnavigate}
  current={active}
  class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors {active
    ? 'bg-slate-200 text-slate-900 dark:bg-slate-700 dark:text-slate-50'
    : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-100'}"
>
  {label}
</AppLink>
