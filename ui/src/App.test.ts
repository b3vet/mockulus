// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';
import { createApi, type Api } from './lib/api.svelte';
import { adminError, fakeClient, stubMapping } from './lib/testing';

/**
 * Covers the wiring the unit tests cannot: that the app mounts at all, and that
 * the router, the route table, the api and the views are connected to each
 * other.
 *
 * Note that `import.meta.env.BASE_URL` is `/` under vitest rather than the
 * sub-path the server uses, so these tests say nothing about base handling —
 * that is what the explicit-base cases in `lib/router.test.ts` are for.
 */
describe('App', () => {
  beforeEach(() => {
    sessionStorage.clear();
    window.history.pushState({}, '', '/');
  });

  /** A deployment that answers everything, which is the unauthenticated default. */
  function openDeployment(): Api {
    const client: MockulusClient = fakeClient({
      system: {
        version: () => Promise.resolve({ version: 'v1.2.3', guessedWireMockVersion: '3.x-subset' }),
      } as Partial<MockulusClient['system']>,
      mappings: {
        paginate: async function* (): AsyncGenerator<StubMapping, void, void> {
          yield stubMapping(1);
        },
        getOrNull: () => Promise.resolve(stubMapping(1)),
      } as Partial<MockulusClient['mappings']>,
    });
    return createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  }

  it('mounts and renders the route matching the current location', () => {
    render(App, { api: openDeployment() });

    expect(screen.getByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument();
  });

  it('swaps the view on nav click, without leaving the page', async () => {
    const user = userEvent.setup();
    render(App, { api: openDeployment() });

    await user.click(screen.getByRole('link', { name: 'About' }));

    expect(screen.getByRole('heading', { level: 1, name: 'About' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { level: 1, name: 'Overview' })).not.toBeInTheDocument();
    expect(window.location.pathname).toBe('/about');
  });

  it('falls back to a not-found view for a path no route claims', () => {
    window.history.pushState({}, '', '/no-such-view');
    render(App, { api: openDeployment() });

    expect(screen.getByRole('heading', { level: 1, name: 'Not found' })).toBeInTheDocument();
  });

  it('renders what the admin API answered, through the SDK', async () => {
    render(App, { api: openDeployment() });

    expect(await screen.findByText('v1.2.3')).toBeInTheDocument();
    expect(screen.getByText('3.x-subset')).toBeInTheDocument();
  });

  it('routes a deep link to a stub, and keeps the section marked in the nav', async () => {
    window.history.pushState({}, '', '/stubs/00000000-0000-4000-8000-000000000001');
    render(App, { api: openDeployment() });

    await screen.findByRole('heading', { level: 1 });

    // Scoped to the navigation: the overview links to the same areas, so an
    // unscoped query by name is ambiguous the moment a page mentions one.
    const nav = screen.getByRole('navigation');
    expect(within(nav).getByRole('link', { name: 'Stubs' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    expect(screen.getByText('/api/things/1')).toBeInTheDocument();
  });

  it('never shows the token sheet to a deployment that has no token', async () => {
    const api = openDeployment();
    render(App, { api });

    await screen.findByText('v1.2.3');

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(api.tokenRequested).toBe(false);
    expect(screen.queryByText('Admin token set for this tab')).not.toBeInTheDocument();
  });

  it('opens the token sheet on a 401, and takes the token without a reload', async () => {
    const user = userEvent.setup();
    let authorized = false;
    const client: MockulusClient = fakeClient({
      system: {
        version: () =>
          authorized
            ? Promise.resolve({ version: 'v1.2.3', guessedWireMockVersion: '3.x-subset' })
            : Promise.reject(adminError(401, [{ code: 10, title: 'Unauthorized' }])),
      } as Partial<MockulusClient['system']>,
    });
    const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
    render(App, { api });

    await screen.findByRole('dialog', { name: 'Admin token required' });

    authorized = true;
    await user.type(screen.getByLabelText('Token'), 's3cret');
    await user.click(screen.getByRole('button', { name: 'Use token' }));

    expect(await screen.findByText('v1.2.3')).toBeInTheDocument();
    expect(sessionStorage.getItem('mockulus.admin-token')).toBe('s3cret');
    expect(screen.getByText('Admin token set for this tab')).toBeInTheDocument();
  });

  it('moves focus into the main region on a route change, and announces the page', async () => {
    const user = userEvent.setup();
    render(App, { api: openDeployment() });

    // Nothing is announced on the first render: the browser has already said
    // what it loaded, and the app repeating it would talk over the platform.
    const live = document.querySelector('[aria-live="polite"].sr-only');
    expect(live).toHaveTextContent('');

    await user.click(screen.getByRole('link', { name: 'About' }));

    // The link that caused the move is still in the document here, so this is
    // the milder half of the problem. The one that matters is a control that
    // unmounts with its view — focus would fall to <body> and the next Tab
    // would start again from the top of the page.
    expect(document.activeElement).toBe(screen.getByRole('main'));
    expect(live).toHaveTextContent('About');
  });

  it('gives the skip link a target focus can actually land on', () => {
    render(App, { api: openDeployment() });

    const skip = screen.getByRole('link', { name: 'Skip to content' });
    const main = screen.getByRole('main');

    expect(skip).toHaveAttribute('href', `#${main.id}`);
    // Without a tabindex the element is not a focus target at all, and the skip
    // link moves the reading position in some browsers and nothing in others.
    expect(main).toHaveAttribute('tabindex', '-1');
  });

  it('titles the document after the route, so a tab is identifiable', async () => {
    const user = userEvent.setup();
    render(App, { api: openDeployment() });

    expect(document.title).toBe('Overview · mockulus');

    await user.click(within(screen.getByRole('navigation')).getByRole('link', { name: 'Stubs' }));

    expect(document.title).toBe('Stubs · mockulus');
  });
});
