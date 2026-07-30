// SPDX-License-Identifier: Apache-2.0
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';

/**
 * Covers the wiring the unit tests cannot: that the app mounts at all, and that
 * the router, the route table and the views are connected to each other.
 *
 * Note that `import.meta.env.BASE_URL` is `/` under vitest rather than the
 * sub-path the server uses, so these tests say nothing about base handling —
 * that is what the explicit-base cases in `lib/router.test.ts` are for.
 */
describe('App', () => {
  beforeEach(() => {
    window.history.pushState({}, '', '/');
  });

  it('mounts and renders the route matching the current location', () => {
    render(App);

    expect(screen.getByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument();
  });

  it('swaps the view on nav click, without leaving the page', async () => {
    const user = userEvent.setup();
    render(App);

    await user.click(screen.getByRole('link', { name: 'About' }));

    expect(screen.getByRole('heading', { level: 1, name: 'About' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { level: 1, name: 'Overview' })).not.toBeInTheDocument();
    expect(window.location.pathname).toBe('/about');
  });

  it('falls back to a not-found view for a path no route claims', () => {
    window.history.pushState({}, '', '/no-such-view');
    render(App);

    expect(screen.getByRole('heading', { level: 1, name: 'Not found' })).toBeInTheDocument();
  });
});
