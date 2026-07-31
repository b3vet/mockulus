// SPDX-License-Identifier: Apache-2.0

import js from '@eslint/js';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import prettier from 'eslint-config-prettier';

export default tseslint.config(
  // The generated types are formatted by the generator and checked by
  // `tsc`; linting them would report on openapi-typescript's output style,
  // which nobody here can act on.
  { ignores: ['dist/', '*.tsbuildinfo', 'src/generated/'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.node },
    },
  },
  prettier,
);
