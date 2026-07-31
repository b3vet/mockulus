<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { shouldRouteClick } from '../router';

  interface Props {
    /** The real URL, so the browser's own navigation affordances still work. */
    href: string;
    /** Marks the link as the one describing the current page. */
    current?: boolean;
    class?: string;
    /**
     * Called instead of letting the browser navigate. Absent means the anchor
     * behaves like an ordinary one, which is the honest fallback for a link
     * rendered outside the shell.
     */
    onnavigate?: (href: string) => void;
    children: Snippet;
  }

  let { href, current = false, class: className = '', onnavigate, children }: Props = $props();

  function handleClick(event: MouseEvent) {
    if (!shouldRouteClick(event) || !onnavigate) {
      return;
    }
    event.preventDefault();
    onnavigate(href);
  }
</script>

<a {href} onclick={handleClick} aria-current={current ? 'page' : undefined} class={className}>
  {@render children()}
</a>
