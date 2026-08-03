// SPDX-License-Identifier: Apache-2.0

/**
 * What a tab list is made of, and how the keyboard moves inside one.
 *
 * The rule below is the part of "a tab" that is not decoration: the ARIA
 * authoring practices make a tab list *one* stop in the page's tab order, with
 * the arrow keys moving between the tabs inside it. Getting that wrong produces
 * a control that looks like tabs and behaves like a row of buttons, which is a
 * worse outcome than plain buttons — it promises a keyboard model it does not
 * honour.
 *
 * This was `journal-tablist.ts` while two stages were building the two surfaces
 * that have a tab list, under that prefix because a neutral name would have been
 * a shared module the other stage had to be told about. `components/TabList`
 * is the shared layer it said it was waiting for, and this is that component's
 * arithmetic — kept out of the component so it can be tested without a DOM.
 */

/** One tab: the value it selects, and what it is called on screen. */
export interface TabDefinition<Id extends string = string> {
  readonly id: Id;
  readonly label: string;
}

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
