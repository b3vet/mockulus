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
  }

  let { api, router, view: View }: Props = $props();

  setApi(untrack(() => api));
  setRouter(untrack(() => router));
</script>

<View />
