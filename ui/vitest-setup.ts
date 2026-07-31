// SPDX-License-Identifier: Apache-2.0
import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/svelte';
import { afterEach } from 'vitest';

// Testing Library's auto-cleanup hooks into globals that are not installed
// here, so unmount explicitly. Without this a component from one test is still
// in the document during the next one.
//
// The body attributes go with it. A modal dialog takes the page out of the
// pointer and scroll flow while it is open and puts it back when it closes, and
// that restoration is scheduled rather than synchronous — so unmounting a test
// mid-dialog can leave `pointer-events: none` on the body, and the next test in
// the file fails to click anything for a reason that has nothing to do with it.
afterEach(() => {
  cleanup();
  document.body.removeAttribute('style');
  document.body.removeAttribute('data-scroll-locked');
});
