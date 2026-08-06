<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { untrack, type Component } from 'svelte';
  import { setApi, type Api } from './api.svelte';
  import { setRouter, type Router } from './router.svelte';

  /**
   * What `App` does for a view, for a test that wants one view and not the
   * whole app: puts a real api and a real router in context and mounts it.
   *
   * The alternative was exporting the two context keys so a test could assemble
   * the map itself, which would widen the module's surface for no reason other
   * than testing and would let any component reach past `getApi()`. This file
   * is imported by tests alone, so nothing of it reaches the bundle.
   */
  interface Props {
    api: Api;
    router: Router<{ readonly path: string }>;
    view: Component;
    /**
     * Props for the mounted component. The shell mounts route views with none,
     * which is why this defaults to none — but a component that takes them, such
     * as the import panel, is still reached through the api and router in
     * context and belongs here rather than in a second host.
     */
    viewProps?: Record<string, unknown>;
  }

  let { api, router, view: View, viewProps = {} }: Props = $props();

  setApi(untrack(() => api));
  setRouter(untrack(() => router));
</script>

<View {...viewProps} />
