// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import StubDetail from './StubDetail.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi } from '../lib/api.svelte';
import { createRouter } from '../lib/router.svelte';
import { adminError, fakeClient, stubMapping } from '../lib/testing';

function mountWith(id: string, mappings: Partial<MockulusClient['mappings']>) {
  window.history.pushState({}, '', `/stubs/${id}`);
  const client: MockulusClient = fakeClient({ mappings });
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/stubs/:id' }]);
  render(TestHost, { api, router, view: StubDetail });
  return { api, router };
}

function mount(id: string, getOrNull: (stubId: string) => Promise<StubMapping | null>) {
  return mountWith(id, { getOrNull } as Partial<MockulusClient['mappings']>);
}

describe('StubDetail', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('asks for the stub named in the path', async () => {
    const getOrNull = vi.fn().mockResolvedValue(stubMapping(1));
    mount('00000000-0000-4000-8000-000000000001', getOrNull);

    await screen.findByRole('heading', { level: 1 });

    expect(getOrNull).toHaveBeenCalledWith('00000000-0000-4000-8000-000000000001');
  });

  it('shows the criteria a reader is looking for, and the document behind them', async () => {
    mount('x', () =>
      Promise.resolve(
        stubMapping(1, {
          name: 'orders lookup',
          priority: 1,
          persistent: true,
          request: { method: 'POST', urlPathTemplate: '/api/orders/{id}' },
          response: { status: 201 },
        }),
      ),
    );

    await screen.findByRole('heading', { level: 1 });

    expect(screen.getByText('POST')).toBeInTheDocument();
    expect(screen.getByText('/api/orders/{id}')).toBeInTheDocument();
    expect(screen.getByText('urlPathTemplate')).toBeInTheDocument();
    expect(screen.getByText('orders lookup')).toBeInTheDocument();
    expect(screen.getByText('201')).toBeInTheDocument();
    expect(screen.getByText('yes')).toBeInTheDocument();
    // The whole stored document, which is what the editor will later edit.
    expect(screen.getByText(/"urlPathTemplate": "\/api\/orders\/\{id\}"/)).toBeInTheDocument();
  });

  it('reads a bodyless 404 as an ordinary absence rather than an error', async () => {
    mount('missing', () => Promise.resolve(null));

    expect(
      await screen.findByRole('heading', { name: 'No stub with that id' }),
    ).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('renders a genuine failure as an error, with a way to retry it', async () => {
    const getOrNull = vi
      .fn()
      .mockRejectedValueOnce(adminError(503, [{ code: 1020, title: 'Store unavailable' }]));
    mount('x', getOrNull);

    expect(
      await screen.findByRole('heading', { name: 'The stub store is unavailable' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();
  });

  it('re-reads when the path moves to another stub, rather than showing the first one', async () => {
    const getOrNull = vi
      .fn()
      .mockImplementation((id: string) =>
        Promise.resolve(stubMapping(1, { name: `stub ${id}`, id })),
      );
    const { router } = mount('first', getOrNull);

    expect(await screen.findByText('stub first')).toBeInTheDocument();

    router.navigate('/stubs/second');

    expect(await screen.findByText('stub second')).toBeInTheDocument();
    expect(getOrNull).toHaveBeenLastCalledWith('second');
  });

  it('links to the editor on this stub, in both of its modes', async () => {
    mount('the-id', () => Promise.resolve(stubMapping(1, { id: 'the-id' })));

    await screen.findByRole('heading', { level: 1 });

    expect(screen.getByRole('link', { name: 'Edit' })).toHaveAttribute(
      'href',
      '/stubs/the-id/edit',
    );
    expect(screen.getByRole('link', { name: 'Duplicate' })).toHaveAttribute(
      'href',
      '/stubs/the-id/duplicate',
    );
  });

  it('asks before deleting, and deletes nothing if the answer is no', async () => {
    const user = userEvent.setup();
    const remove = vi.fn().mockResolvedValue(undefined);
    mountWith('the-id', {
      getOrNull: () => Promise.resolve(stubMapping(1, { id: 'the-id' })),
      delete: remove,
    });
    await screen.findByRole('heading', { level: 1 });

    await user.click(screen.getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Delete this stub?')).toBeInTheDocument();
    // Focus lands on Cancel rather than on the destructive button, so the reflex
    // answer to a dialog nobody read is the one that changes nothing.
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    expect(remove).not.toHaveBeenCalled();
  });

  it('gives focus back to the control that opened the dialog', async () => {
    const user = userEvent.setup();
    mountWith('the-id', {
      getOrNull: () => Promise.resolve(stubMapping(1, { id: 'the-id' })),
      delete: vi.fn(),
    });
    await screen.findByRole('heading', { level: 1 });

    const trigger = screen.getByRole('button', { name: 'Delete' });
    await user.click(trigger);
    await screen.findByRole('dialog');

    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    // The dialog is the trigger's own, which is what gives the keyboard
    // somewhere to come back to. A button elsewhere on the page that merely set
    // a flag would drop the reader at the top of the document.
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it('deletes on confirmation and returns to the list, which no longer holds the stub', async () => {
    const user = userEvent.setup();
    const remove = vi.fn().mockResolvedValue(undefined);
    mountWith('the-id', {
      getOrNull: () => Promise.resolve(stubMapping(1, { id: 'the-id' })),
      delete: remove,
    });
    await screen.findByRole('heading', { level: 1 });

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete the stub' }),
    );

    await waitFor(() => expect(remove).toHaveBeenCalledWith('the-id'));
    await waitFor(() => expect(window.location.pathname).toBe('/stubs'));
  });

  it('keeps the reader on the stub when the delete is refused, and explains why', async () => {
    const user = userEvent.setup();
    const remove = vi
      .fn()
      .mockRejectedValue(adminError(503, [{ code: 1020, title: 'Store unavailable' }]));
    mountWith('the-id', {
      getOrNull: () => Promise.resolve(stubMapping(1, { id: 'the-id' })),
      delete: remove,
    });
    await screen.findByRole('heading', { level: 1 });

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete the stub' }),
    );

    expect(
      await screen.findByRole('heading', { name: 'The stub store is unavailable' }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe('/stubs/the-id');
  });
});
