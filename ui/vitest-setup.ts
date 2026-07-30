// SPDX-License-Identifier: Apache-2.0
import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/svelte';
import { afterEach } from 'vitest';

// Testing Library's auto-cleanup hooks into globals that are not installed
// here, so unmount explicitly. Without this a component from one test is still
// in the document during the next one.
afterEach(() => {
  cleanup();
});
