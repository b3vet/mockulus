// SPDX-License-Identifier: Apache-2.0
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// Kept in its own file rather than inlined into vite.config.ts because
// svelte-check, eslint-plugin-svelte and the Vite plugin all read it
// independently, and they must agree on the preprocessor.
export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    // Runes everywhere, with no legacy-syntax fallback: a component that
    // reaches for `export let` or `$:` should fail to compile rather than
    // quietly opt the file out of the reactivity model the rest uses.
    runes: true,
  },
};
