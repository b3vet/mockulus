// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import StubDetail from './StubDetail.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi } from '../lib/api.svelte';
import { createRouter } from '../lib/router.svelte';
import { adminError, fakeClient, stubMapping } from '../lib/testing';

function mount(id: string, getOrNull: (stubId: string) => Promise<StubMapping | null>) {
  window.history.pushState({}, '', `/stubs/${id}`);
  const client: MockulusClient = fakeClient({
    mappings: { getOrNull } as Partial<MockulusClient['mappings']>,
  });
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/stubs/:id' }]);
  render(TestHost, { api, router, view: StubDetail });
  return { api, router };
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
});
