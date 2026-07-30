<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts">
  import { toHref } from '../lib/router';
</script>

<h1 class="text-2xl font-semibold tracking-tight">Not found</h1>

<p class="mt-3 max-w-2xl text-slate-600 dark:text-slate-400">
  No view is mounted at this path. The server answered with the app itself — that is the SPA
  fallback working — but the router has nothing to show for it.
</p>

<a
  href={toHref('/')}
  class="mt-6 inline-block text-sm font-medium text-sky-700 underline underline-offset-4 hover:text-sky-900 dark:text-sky-400 dark:hover:text-sky-300"
>
  Back to the overview
</a>
