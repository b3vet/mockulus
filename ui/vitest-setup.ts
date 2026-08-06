// SPDX-License-Identifier: Apache-2.0
import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/svelte';
import { afterEach } from 'vitest';

// jsdom implements `Range` but not its geometry: `getClientRects` and
// `getBoundingClientRect` are simply absent from the prototype. CodeMirror
// measures the document through both on every layout pass, from inside a
// `requestAnimationFrame` callback — so without these the stub editor throws
// where no test can catch it, and every later test in the file inherits an
// unhandled error that has nothing to do with what it is checking.
//
// Empty geometry is the honest answer in a DOM that performs no layout, and it
// is enough for what these tests assert: the document's text, the selection and
// where focus is, none of which depends on measurement. A test that needed real
// geometry would need a real browser, which is what the Playwright smoke the SOW
// schedules for the hardening stage is for.
if (typeof Range !== 'undefined' && Range.prototype.getClientRects === undefined) {
  const emptyRect = () => ({
    top: 0,
    right: 0,
    bottom: 0,
    left: 0,
    width: 0,
    height: 0,
    x: 0,
    y: 0,
  });
  Range.prototype.getClientRects = function () {
    const rects: ReturnType<typeof emptyRect>[] = [];
    return Object.assign(rects, { item: () => null }) as unknown as DOMRectList;
  };
  Range.prototype.getBoundingClientRect = function () {
    return emptyRect() as DOMRect;
  };
}

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
