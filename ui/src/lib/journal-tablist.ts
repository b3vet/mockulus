// SPDX-License-Identifier: Apache-2.0

/**
 * Keyboard movement within a tab list, which the ARIA authoring practices make
 * part of what "a tab" means: a tab list is one stop in the tab order, and the
 * arrow keys move between the tabs inside it.
 *
 * Both surfaces this stage builds have a tab list — the journal's three
 * outcomes, the near-miss debugger's two modes — so the rule is written once.
 * It lives under a `journal-` name because this stage owns that prefix and a
 * neutral `tablist.ts` would be a shared module the parallel stage building the
 * other half of this UI would have to be told about; when a shared primitive
 * layer exists, this is the first thing that belongs in it.
 */

/**
 * Which tab a key press moves to, or `undefined` when the key is not one this
 * list handles and the browser should keep it.
 *
 * Movement wraps, which is the behaviour the authoring practices describe and
 * the one that makes a three-item list navigable without looking: Right from
 * the last tab is the first tab rather than nothing happening.
 */
export function tablistTargetIndex(
  key: string,
  current: number,
  count: number,
): number | undefined {
  if (count <= 0) {
    return undefined;
  }
  switch (key) {
    case 'ArrowRight':
      return (current + 1) % count;
    case 'ArrowLeft':
      return (current - 1 + count) % count;
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
    default:
      return undefined;
  }
}
