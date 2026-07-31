// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    projects: [
      {
        // Pure logic: URL building, error mapping, the shape of what the
        // builders emit. No server, no ports, no ordering.
        test: { name: 'unit', include: ['test/unit/**/*.test.ts'] },
      },
      {
        // Driven against a real mockulus process the suite starts itself.
        // Separate because it is the slow half and because a machine without a
        // built binary should be told that rather than shown a wall of
        // connection refusals.
        test: {
          name: 'integration',
          include: ['test/integration/**/*.test.ts'],
          testTimeout: 30_000,
          hookTimeout: 60_000,
          // One server, shared: these cases namespace their stubs by URL prefix
          // rather than resetting, which is the discipline SPEC §1 asks users
          // for and the SDK's own test helpers will encode.
          fileParallelism: false,
        },
      },
    ],
  },
});
