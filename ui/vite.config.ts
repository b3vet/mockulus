// SPDX-License-Identifier: Apache-2.0
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { existsSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

/**
 * Where the Go side expects the built site. `internal/adminui` embeds this
 * directory with `//go:embed all:dist`, so the build output is not an artifact
 * that happens to live near the server — it is an input to `go build`.
 */
const embedDir = fileURLToPath(new URL('../internal/adminui/dist', import.meta.url));

/**
 * The sub-path the admin mux serves the UI under. Everything downstream depends
 * on it: asset URLs in index.html, the router's path normalisation, and the
 * dev-server proxy exclusion below.
 */
const uiBase = '/__admin/mockulus/ui/';

/**
 * `internal/adminui/dist/.gitkeep` is a committed placeholder, not build
 * output: it is what lets `go build` succeed on a machine with no Node, since
 * `go:embed` refuses to embed a directory that does not exist. `emptyOutDir`
 * cannot tell it apart from a stale asset, so we put it back afterwards.
 */
function keepEmbedPlaceholder(): Plugin {
  return {
    name: 'mockulus:keep-embed-placeholder',
    apply: 'build',
    closeBundle() {
      const placeholder = `${embedDir}/.gitkeep`;
      if (!existsSync(placeholder)) {
        writeFileSync(placeholder, '');
      }
    },
  };
}

export default defineConfig({
  base: uiBase,
  plugins: [tailwindcss(), svelte(), keepEmbedPlaceholder()],
  build: {
    outDir: embedDir,
    emptyOutDir: true,
  },
  server: {
    // Talk to a locally running mockulus (`make run`) so the dev loop has the
    // same origin semantics as production, where the admin mux serves both the
    // assets and the API. The negative lookahead keeps the dev server's own
    // base path — which lives under /__admin too — out of the proxy.
    proxy: {
      '^/__admin/(?!mockulus/ui/)': {
        target: 'http://127.0.0.1:9090',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./vitest-setup.ts'],
    include: ['src/**/*.test.ts'],
  },
  // Testing Library mounts components in jsdom, which needs Svelte's browser
  // build rather than its SSR one. Only under vitest — overriding the
  // resolution conditions for a real build would be a different, worse idea.
  resolve: process.env.VITEST ? { conditions: ['browser'] } : {},
});
