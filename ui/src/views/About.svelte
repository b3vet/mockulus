<!-- SPDX-License-Identifier: Apache-2.0 -->
<h1 class="text-2xl font-semibold tracking-tight">About</h1>

<p class="mt-3 max-w-2xl text-slate-600 dark:text-slate-400">
  mockulus is a WireMock-compatible mock server. This UI ships inside the binary and is served by
  the admin mux under the reserved
  <code class="font-mono text-sm">/__admin/mockulus/</code> extension namespace, so it never collides
  with a WireMock-compatible route.
</p>

<dl class="mt-8 grid gap-x-8 gap-y-4 sm:grid-cols-[max-content_1fr]">
  <dt class="text-sm font-semibold text-slate-500 dark:text-slate-400">Served at</dt>
  <dd class="font-mono text-sm">{import.meta.env.BASE_URL}</dd>

  <dt class="text-sm font-semibold text-slate-500 dark:text-slate-400">Routing</dt>
  <dd class="text-sm">
    History mode, backed by a server-side SPA fallback — a deep link survives a reload.
  </dd>

  <dt class="text-sm font-semibold text-slate-500 dark:text-slate-400">API access</dt>
  <dd class="text-sm">
    Through the public admin API only. The UI gets no private endpoints and keeps no server-side
    session.
  </dd>
</dl>
