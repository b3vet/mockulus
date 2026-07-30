<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  interface Props {
    /** The real URL, so the browser's own navigation affordances still work. */
    href: string;
    label: string;
    active?: boolean;
    onnavigate?: (href: string) => void;
  }

  let { href, label, active = false, onnavigate }: Props = $props();

  function handleClick(event: MouseEvent) {
    // Anything that is not a plain left-click — a modifier, the middle button —
    // is the user asking the browser for a new tab or window. Leave it alone.
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return;
    }
    event.preventDefault();
    onnavigate?.(href);
  }
</script>

<a
  {href}
  onclick={handleClick}
  aria-current={active ? 'page' : undefined}
  class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors {active
    ? 'bg-slate-200 text-slate-900 dark:bg-slate-700 dark:text-slate-50'
    : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-100'}"
>
  {label}
</a>
