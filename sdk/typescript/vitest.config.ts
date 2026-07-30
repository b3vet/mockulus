// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    // The unit lane covers pure logic. The integration lane, which drives a
    // real mockulus process, arrives with the client it would exercise.
    include: ['test/unit/**/*.test.ts'],
  },
});
