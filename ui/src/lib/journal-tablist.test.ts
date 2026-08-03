// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { tablistTargetIndex } from './journal-tablist';

describe('tablistTargetIndex', () => {
  it('moves one tab in the direction of the arrow', () => {
    expect(tablistTargetIndex('ArrowRight', 0, 3)).toBe(1);
    expect(tablistTargetIndex('ArrowLeft', 2, 3)).toBe(1);
  });

  it('wraps at both ends, so the list is navigable without looking', () => {
    expect(tablistTargetIndex('ArrowRight', 2, 3)).toBe(0);
    expect(tablistTargetIndex('ArrowLeft', 0, 3)).toBe(2);
  });

  it('jumps to the ends on Home and End', () => {
    expect(tablistTargetIndex('Home', 2, 3)).toBe(0);
    expect(tablistTargetIndex('End', 0, 3)).toBe(2);
  });

  it('leaves every other key to the browser', () => {
    // Tab, in particular. A tab list is one stop in the page's tab order, and
    // swallowing Tab here would trap the reader inside it.
    expect(tablistTargetIndex('Tab', 0, 3)).toBeUndefined();
    expect(tablistTargetIndex('Enter', 0, 3)).toBeUndefined();
    expect(tablistTargetIndex('ArrowDown', 0, 3)).toBeUndefined();
  });

  it('has nowhere to move in an empty list', () => {
    expect(tablistTargetIndex('ArrowRight', 0, 0)).toBeUndefined();
  });
});
