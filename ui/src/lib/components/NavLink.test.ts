// SPDX-License-Identifier: Apache-2.0
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import NavLink from './NavLink.svelte';

describe('NavLink', () => {
  it('renders its label and keeps the real href on the anchor', () => {
    render(NavLink, { href: '/__admin/mockulus/ui/about', label: 'About' });

    const link = screen.getByRole('link', { name: 'About' });
    expect(link).toHaveAttribute('href', '/__admin/mockulus/ui/about');
    expect(link).not.toHaveAttribute('aria-current');
  });

  it('marks the active link for assistive technology', () => {
    render(NavLink, { href: '/__admin/mockulus/ui/', label: 'Overview', active: true });

    expect(screen.getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page');
  });

  it('routes a plain click in-app instead of letting the browser navigate', async () => {
    const onnavigate = vi.fn();
    const user = userEvent.setup();
    render(NavLink, { href: '/__admin/mockulus/ui/about', label: 'About', onnavigate });

    await user.click(screen.getByRole('link', { name: 'About' }));

    expect(onnavigate).toHaveBeenCalledWith('/__admin/mockulus/ui/about');
  });
});
