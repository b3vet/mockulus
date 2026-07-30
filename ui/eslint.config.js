// SPDX-License-Identifier: Apache-2.0
import js from '@eslint/js';
import prettier from 'eslint-config-prettier';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';
import svelteConfig from './svelte.config.js';

export default ts.config(
  {
    // Build output and the embed directory are generated; nothing in them is
    // ours to lint.
    ignores: ['dist/', '../internal/adminui/dist/'],
  },
  js.configs.recommended,
  ts.configs.recommended,
  svelte.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.browser },
    },
  },
  {
    files: ['**/*.svelte', '**/*.svelte.ts'],
    languageOptions: {
      parserOptions: {
        // The Svelte parser hands <script lang="ts"> blocks to the TS parser,
        // and needs the project's svelte.config.js to agree with it about
        // preprocessing and the runes-only compiler setting.
        parser: ts.parser,
        svelteConfig,
      },
    },
  },
  {
    files: ['vite.config.ts', 'vitest-setup.ts', 'eslint.config.js', 'svelte.config.js'],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
  // Last: turns off the stylistic rules prettier owns, so the two never
  // disagree about the same line.
  prettier,
);
