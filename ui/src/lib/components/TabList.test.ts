// SPDX-License-Identifier: Apache-2.0
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import TabList from './TabList.svelte';

/**
 * The shared primitive's own contract.
 *
 * The two surfaces that use it already assert their own behaviour end to end —
 * the journal's outcomes and the near-miss debugger's modes each cover arrow
 * movement through the real view. What those cannot say is anything about a
 * list with more than three tabs, or about the parts of the keyboard model that
 * are the same everywhere: this is where the primitive is held to the authoring
 * practices rather than to either caller's use of it.
 */
describe('TabList', () => {
  const TABS = [
    { id: 'one', label: 'One' },
    { id: 'two', label: 'Two' },
    { id: 'three', label: 'Three' },
    { id: 'four', label: 'Four' },
  ] as const;

  function mount(selected: string = 'one') {
    let current = selected;
    const chosen: string[] = [];
    const view = render(TabList, {
      label: 'Test tabs',
      tabs: TABS,
      selected: current,
      onselect: (id: string) => {
        current = id;
        chosen.push(id);
        void view.rerender({ selected: id });
      },
      tabId: (id: string) => `t-${id}`,
      panelId: () => 'panel',
    });
    return { chosen };
  }

  it('keeps exactly one tab in the page tab order, so the list is one stop', () => {
    mount('two');

    expect(screen.getByRole('tab', { name: 'Two' })).toHaveAttribute('tabindex', '0');
    for (const name of ['One', 'Three', 'Four']) {
      expect(screen.getByRole('tab', { name })).toHaveAttribute('tabindex', '-1');
    }
  });

  it('wraps at both ends, so a list is navigable without looking', async () => {
    const user = userEvent.setup();
    const { chosen } = mount('one');

    await user.click(screen.getByRole('tab', { name: 'One' }));
    await user.keyboard('{ArrowLeft}');

    expect(chosen.at(-1)).toBe('four');
    expect(screen.getByRole('tab', { name: 'Four' })).toHaveFocus();

    await user.keyboard('{ArrowRight}');

    expect(chosen.at(-1)).toBe('one');
    expect(screen.getByRole('tab', { name: 'One' })).toHaveFocus();
  });

  it('jumps to the ends on Home and End', async () => {
    const user = userEvent.setup();
    const { chosen } = mount('one');

    await user.click(screen.getByRole('tab', { name: 'One' }));
    await user.keyboard('{End}');

    expect(chosen.at(-1)).toBe('four');
    expect(screen.getByRole('tab', { name: 'Four' })).toHaveFocus();

    await user.keyboard('{Home}');

    expect(chosen.at(-1)).toBe('one');
    expect(screen.getByRole('tab', { name: 'One' })).toHaveFocus();
  });

  it('leaves Tab to the browser, so the reader can get out of the list', async () => {
    const user = userEvent.setup();
    const { chosen } = mount('one');

    await user.click(screen.getByRole('tab', { name: 'One' }));
    await user.keyboard('{Tab}');

    // Nothing was selected by the press, and focus left the list rather than
    // moving inside it — a tab list that swallowed Tab would be the one control
    // on the page a keyboard user could not leave.
    expect(chosen).toEqual(['one']);
    expect(screen.getByRole('tab', { name: 'One' })).not.toHaveFocus();
  });

  it('names every tab to its panel and back, which is what pairs the two', () => {
    mount('three');

    const tab = screen.getByRole('tab', { name: 'Three' });
    expect(tab).toHaveAttribute('id', 't-three');
    expect(tab).toHaveAttribute('aria-controls', 'panel');
    expect(tab).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tablist')).toHaveAccessibleName('Test tabs');
  });
});
