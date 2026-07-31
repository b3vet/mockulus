// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import Stubs from './Stubs.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi, type Api } from '../lib/api.svelte';
import { createRouter } from '../lib/router.svelte';
import { PAGE_SIZE } from '../lib/stubs';
import { adminError, fakeClient, stubMapping, stubMappings } from '../lib/testing';

/** A client whose snapshot is exactly these mappings, walked as `paginate` walks one. */
function clientOver(
  mappings: readonly StubMapping[],
  overrides: Partial<MockulusClient['mappings']> = {},
): MockulusClient {
  return fakeClient({
    mappings: {
      paginate: async function* () {
        yield* mappings;
      },
      ...overrides,
    } as Partial<MockulusClient['mappings']>,
  });
}

function mount(client: MockulusClient): { api: Api } {
  window.history.pushState({}, '', '/stubs');
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/stubs' }]);
  render(TestHost, { api, router, view: Stubs });
  return { api };
}

/** The list itself, so a count is of rows and not of every link on the page. */
const rows = () => within(screen.getByRole('list', { name: 'Stub mappings' })).getAllByRole('link');

describe('Stubs', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('lists the stubs the replica compiled, one row each', async () => {
    mount(clientOver(stubMappings(3)));

    await screen.findByRole('list', { name: 'Stub mappings' });

    expect(rows()).toHaveLength(3);
    expect(screen.getByText('/api/things/1')).toBeInTheDocument();
    expect(screen.getByText('Showing 1–3 of 3')).toBeInTheDocument();
  });

  it('links each row to that stub, so the detail view is one click and one URL away', async () => {
    mount(clientOver([stubMapping(1)]));

    await screen.findByRole('list', { name: 'Stub mappings' });

    expect(rows()[0]).toHaveAttribute('href', '/stubs/00000000-0000-4000-8000-000000000001');
  });

  it('makes every row reachable by keyboard and nameable by a screen reader', async () => {
    mount(
      clientOver([
        stubMapping(1),
        // A stub whose only description is its criteria: no name to fall back on,
        // which is where a row built out of decoration rather than text would
        // end up announcing nothing.
        stubMapping(2, { name: undefined, request: { method: 'ANY' } }),
      ]),
    );

    await screen.findByRole('list', { name: 'Stub mappings' });

    for (const row of rows()) {
      expect(row).toHaveAccessibleName();
      expect(row.tabIndex).toBe(0);
    }
  });

  it('renders one page of a large snapshot rather than all of it', async () => {
    mount(clientOver(stubMappings(4321)));

    await screen.findByRole('list', { name: 'Stub mappings' });

    expect(rows()).toHaveLength(PAGE_SIZE);
    // The total is grouped for the reader's locale, so the expectation is built
    // the same way rather than pinned to one separator.
    expect(screen.getByText(`Showing 1–50 of ${(4321).toLocaleString()}`)).toBeInTheDocument();
    expect(screen.getByText('Page 1 of 87')).toBeInTheDocument();
  });

  it('pages through the set without asking the server again', async () => {
    const user = userEvent.setup();
    const paginate = vi.fn(async function* () {
      yield* stubMappings(120);
    });
    mount(clientOver([], { paginate } as Partial<MockulusClient['mappings']>));

    await screen.findByRole('list', { name: 'Stub mappings' });
    await user.click(screen.getByRole('button', { name: 'Next page' }));

    expect(screen.getByText('Showing 51–100 of 120')).toBeInTheDocument();
    expect(paginate).toHaveBeenCalledTimes(1);
  });

  it('stops the pager at both ends', async () => {
    const user = userEvent.setup();
    mount(clientOver(stubMappings(60)));

    await screen.findByRole('list', { name: 'Stub mappings' });
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeEnabled();
  });

  it('narrows by method, and offers only the methods that are there', async () => {
    const user = userEvent.setup();
    mount(
      clientOver([
        stubMapping(1, { request: { method: 'GET', urlPath: '/api/orders' } }),
        stubMapping(2, { request: { method: 'POST', urlPath: '/api/orders' } }),
      ]),
    );

    await screen.findByRole('list', { name: 'Stub mappings' });
    const method = screen.getByLabelText('Method');
    expect(
      within(method)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Any method', 'GET', 'POST']);

    await user.selectOptions(method, 'POST');

    expect(rows()).toHaveLength(1);
    expect(screen.getByText('Showing 1–1 of 1 (filtered from 2)')).toBeInTheDocument();
  });

  it('narrows by a URL substring across the whole snapshot, not the visible page', async () => {
    const user = userEvent.setup();
    const many = [
      ...stubMappings(60),
      stubMapping(999, { request: { method: 'GET', urlPath: '/api/payments' } }),
    ];
    mount(clientOver(many));

    await screen.findByRole('list', { name: 'Stub mappings' });
    // The payments stub is on page two, which is what makes this the test that
    // a page-local filter would fail.
    expect(screen.queryByText('/api/payments')).not.toBeInTheDocument();

    await user.type(screen.getByLabelText('URL contains'), 'payments');

    expect(rows()).toHaveLength(1);
    expect(screen.getByText('/api/payments')).toBeInTheDocument();
  });

  it('returns to the first page when a filter changes what there is to page through', async () => {
    const user = userEvent.setup();
    mount(clientOver(stubMappings(120)));

    await screen.findByRole('list', { name: 'Stub mappings' });
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(screen.getByText('Page 2 of 3')).toBeInTheDocument();

    await user.type(screen.getByLabelText('URL contains'), '/api/things/1');

    expect(screen.getByText('Page 1 of 1')).toBeInTheDocument();
  });

  it('sends the metadata search to the server, which is what find-by-metadata is for', async () => {
    const user = userEvent.setup();
    const findByMetadata = vi.fn().mockResolvedValue({
      mappings: [stubMapping(7, { metadata: { team: 'checkout' } })],
      meta: { total: 1 },
    });
    mount(clientOver(stubMappings(3), { findByMetadata } as Partial<MockulusClient['mappings']>));

    await screen.findByRole('list', { name: 'Stub mappings' });
    await user.type(screen.getByLabelText('Metadata (JSONPath)'), '$.team');
    await user.click(screen.getByRole('button', { name: 'Apply' }));

    await screen.findByText('Showing 1–1 of 1');
    expect(findByMetadata).toHaveBeenCalledWith({ matchesJsonPath: '$.team' });
  });

  it('renders the pointers of a refused metadata search rather than a generic message', async () => {
    const user = userEvent.setup();
    const findByMetadata = vi.fn().mockRejectedValue(
      adminError(422, [
        {
          code: 10,
          title: 'Malformed request',
          detail: 'JSONPath does not compile: unclosed [ in "$["',
          source: { pointer: '/matchesJsonPath' },
        },
      ]),
    );
    mount(clientOver(stubMappings(3), { findByMetadata } as Partial<MockulusClient['mappings']>));

    await screen.findByRole('list', { name: 'Stub mappings' });
    // `[[` is how user-event spells a literal `[`, which a bare one would start
    // a key descriptor with. The value that reaches the server is `$[`.
    await user.type(screen.getByLabelText('Metadata (JSONPath)'), '$[[');
    await user.click(screen.getByRole('button', { name: 'Apply' }));

    expect(findByMetadata).toHaveBeenCalledWith({ matchesJsonPath: '$[' });
    expect(
      await screen.findByRole('heading', { name: 'The server refused the request' }),
    ).toBeInTheDocument();
    expect(screen.getByText('/matchesJsonPath')).toBeInTheDocument();
    expect(screen.getByText(/JSONPath does not compile/)).toBeInTheDocument();
  });

  it('tells an empty deployment apart from an over-narrow filter', async () => {
    mount(clientOver([]));

    expect(await screen.findByRole('heading', { name: 'No stubs registered' })).toBeInTheDocument();
    // Nothing to clear, so no button offering to: a deployment with no stubs is
    // not a search that went wrong, and saying so would send the reader looking
    // for a filter they never set.
    expect(screen.queryByRole('button', { name: 'Clear filters' })).not.toBeInTheDocument();
  });

  it('offers a way out of a filter that matches nothing', async () => {
    const user = userEvent.setup();
    mount(clientOver(stubMappings(3)));

    await screen.findByRole('list', { name: 'Stub mappings' });
    await user.type(screen.getByLabelText('URL contains'), 'nothing-matches-this');

    expect(
      screen.getByRole('heading', { name: 'No stub matches these filters' }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Clear filters' }));

    expect(await screen.findByRole('list', { name: 'Stub mappings' })).toBeInTheDocument();
  });

  it('asks for a token when the deployment refuses the read', async () => {
    // The refusal arrives instead of the first page, which is what a
    // token-protected deployment does: the walk rejects having yielded nothing.
    const paginate = async function* (): AsyncGenerator<StubMapping, void, void> {
      const refused: StubMapping[] = await Promise.reject(
        adminError(401, [{ code: 10, title: 'Unauthorized' }]),
      );
      yield* refused;
    };
    const { api } = mount(clientOver([], { paginate } as Partial<MockulusClient['mappings']>));

    expect(
      await screen.findByRole('heading', { name: 'This deployment needs an admin token' }),
    ).toBeInTheDocument();
    expect(api.tokenRequested).toBe(true);
  });
});
