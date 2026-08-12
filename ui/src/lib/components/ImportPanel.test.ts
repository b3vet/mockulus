// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMappingImport } from '@mockulus/admin-sdk';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import type { Component } from 'svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ImportPanel from './ImportPanel.svelte';
import TestHost from '../TestHost.svelte';
import { createApi, type Api } from '../api.svelte';
import { createRouter } from '../router.svelte';
import { adminError, fakeClient, stubMappings } from '../testing';

/**
 * The panel reads the api from context, as every view does, so it is mounted
 * through the same host the views use rather than handed a fake of its own.
 */
function mount(
  mappings: Partial<MockulusClient['mappings']>,
  mockulus: Partial<MockulusClient['mockulus']> = {},
): {
  api: Api;
  onimported: ReturnType<typeof vi.fn>;
} {
  window.history.pushState({}, '', '/stubs');
  const api = createApi({
    baseUrl: 'http://mock.example',
    createClient: () => fakeClient({ mappings, mockulus }),
  });
  const router = createRouter([{ path: '/stubs' }]);
  const onimported = vi.fn();
  // The host types its view as a component taking no props, because every route
  // view is one. This panel is not a route view — it is mounted by the stub list
  // and told when a batch has landed — so the cast is where that difference is
  // paid for, and `viewProps` below is what it is paid with.
  const view = ImportPanel as unknown as Component;
  render(TestHost, { api, router, view, viewProps: { onimported } });
  return { api, onimported };
}

/** Hands the file input a document, as the file picker would. */
async function choose(user: ReturnType<typeof userEvent.setup>, text: string, name = 'batch.json') {
  const input = screen.getByLabelText('Choose a file…');
  await user.upload(input, new File([text], name, { type: 'application/json' }));
}

const goodFile = JSON.stringify({ mappings: stubMappings(3) });

describe('ImportPanel', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('reads a file, says what is in it, and writes nothing until asked', async () => {
    const user = userEvent.setup();
    const load = vi.fn().mockResolvedValue(undefined);
    mount({ import: load } as Partial<MockulusClient['mappings']>);

    await choose(user, goodFile);

    expect(await screen.findByText('batch.json')).toBeInTheDocument();
    expect(load).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Write 3 mappings' }));

    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
    const sent = load.mock.calls[0]?.[0] as StubMappingImport;
    expect(sent.mappings).toHaveLength(3);
  });

  it('re-reads the list after a batch lands, and says how many were written', async () => {
    const user = userEvent.setup();
    const { onimported } = mount({
      import: vi.fn().mockResolvedValue(undefined),
    } as Partial<MockulusClient['mappings']>);

    await choose(user, goodFile);
    await user.click(screen.getByRole('button', { name: 'Write 3 mappings' }));

    expect(await screen.findByText(/Wrote 3 mappings/)).toBeInTheDocument();
    expect(onimported).toHaveBeenCalledTimes(1);
  });

  it('refuses a file the endpoint would refuse, and says what to write instead', async () => {
    const user = userEvent.setup();
    const load = vi.fn();
    mount({ import: load } as Partial<MockulusClient['mappings']>);

    await choose(user, '[{"request": {}}]', 'bare-array.json');

    expect(await screen.findByText(/this file is an array\. Wrap it as/)).toBeInTheDocument();
    expect(load).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: /^Write / })).not.toBeInTheDocument();
  });

  it('says nothing was written when the batch is refused, which is what atomic means', async () => {
    const user = userEvent.setup();
    const load = vi.fn().mockRejectedValue(
      adminError(
        422,
        [
          {
            code: 1000,
            title: 'Unsupported feature',
            detail: 'equalToXml is not supported in mockulus v1 — see ROADMAP.md',
            source: { pointer: '/mappings/1/request/bodyPatterns/0/equalToXml' },
          },
          {
            code: 1003,
            title: 'Invalid regular expression',
            detail: 'pattern does not compile',
            source: { pointer: '/mappings/1/request/urlPattern' },
          },
          {
            code: 10,
            title: 'Malformed request',
            detail: 'method must be a string',
            source: { pointer: '/mappings/2/request/method' },
          },
        ],
        { method: 'POST', path: '/__admin/mappings/import' },
      ),
    );
    mount({ import: load } as Partial<MockulusClient['mappings']>);

    await choose(user, goodFile);
    await user.click(screen.getByRole('button', { name: 'Write 3 mappings' }));

    expect(await screen.findByRole('heading', { name: 'Nothing was written' })).toBeInTheDocument();

    // Grouped back into the mappings they came from: two groups for three
    // problems, because the reader's next move is to open one mapping in the
    // file, not three.
    const rejected = screen.getByRole('list', { name: 'Rejected mappings' });
    expect(within(rejected).getByText(/Mapping 2 of 3 — GET \/api\/things\/2/)).toBeInTheDocument();
    expect(within(rejected).getByText(/Mapping 3 of 3 — GET \/api\/things\/3/)).toBeInTheDocument();
    // The one mapping the server said nothing about is not accused of anything.
    expect(within(rejected).queryByText(/Mapping 1 of 3/)).not.toBeInTheDocument();

    // The index prefix is undone, so the pointer reads as it would against that
    // one mapping — the same spelling the single-stub editor shows.
    expect(screen.getByText('/request/bodyPatterns/0/equalToXml')).toBeInTheDocument();
    expect(screen.getByText('/request/urlPattern')).toBeInTheDocument();

    // And nothing invites a pointless re-send of the same document.
    expect(screen.queryByRole('button', { name: /^Write / })).not.toBeInTheDocument();
  });

  it('sends a 401 to the token flow, and the batch survives it', async () => {
    const user = userEvent.setup();
    let authorized = false;
    const written: StubMappingImport[] = [];
    const load = vi.fn().mockImplementation((batch: StubMappingImport) => {
      if (!authorized) {
        return Promise.reject(adminError(401, [{ code: 10, title: 'Unauthorized' }]));
      }
      written.push(batch);
      return Promise.resolve(undefined);
    });
    const { api } = mount({ import: load } as Partial<MockulusClient['mappings']>);

    await choose(user, goodFile);
    await user.click(screen.getByRole('button', { name: 'Write 3 mappings' }));
    await waitFor(() => expect(api.tokenRequested).toBe(true));

    authorized = true;
    api.submitToken('s3cret');

    await waitFor(() => expect(written).toHaveLength(1));
    expect(written[0]?.mappings).toHaveLength(3);
  });

  describe('checking without writing', () => {
    it('reports a clean file without importing it', async () => {
      const user = userEvent.setup();
      const validate = vi.fn().mockResolvedValue({
        valid: true,
        wouldImport: true,
        summary: { total: 3, valid: 3, invalid: 0 },
        results: [
          { index: 0, valid: true },
          { index: 1, valid: true },
          { index: 2, valid: true },
        ],
      });
      const importMappings = vi.fn();
      mount({ import: importMappings }, { validate });

      await choose(user, goodFile);
      await user.click(screen.getByRole('button', { name: 'Check without writing' }));

      await waitFor(() => {
        expect(screen.getByRole('status')).toHaveTextContent(/All of them would register/);
      });
      expect(validate).toHaveBeenCalledTimes(1);
      // The whole point: checking is not importing.
      expect(importMappings).not.toHaveBeenCalled();
    });

    it('leads with the batch verdict, because import is atomic', async () => {
      const user = userEvent.setup();
      const validate = vi.fn().mockResolvedValue({
        valid: false,
        wouldImport: false,
        summary: { total: 3, valid: 2, invalid: 1 },
        results: [
          { index: 0, valid: true },
          {
            index: 1,
            valid: false,
            errors: [
              {
                code: 1000,
                title: 'Unsupported feature',
                detail: 'equalToXml is not supported in mockulus v1',
                source: { pointer: '/request/bodyPatterns/0/equalToXml' },
              },
            ],
          },
          { index: 2, valid: true },
        ],
      });
      const importMappings = vi.fn();
      mount({ import: importMappings }, { validate });

      await choose(user, goodFile);
      await user.click(screen.getByRole('button', { name: 'Check without writing' }));

      // A reader looking at one bad mapping out of three would otherwise
      // conclude that two would land. None would.
      await waitFor(() => {
        expect(screen.getByRole('status')).toHaveTextContent(/This file would not import/);
      });
      expect(screen.getByRole('status')).toHaveTextContent(/nothing at all would be written/);

      // The offending mapping is named, and its problem is shown where the same
      // problem from a real refusal would be.
      expect(await screen.findByText(/equalToXml is not supported/)).toBeInTheDocument();

      expect(importMappings).not.toHaveBeenCalled();
    });

    it('clears the report when the file is cleared', async () => {
      const user = userEvent.setup();
      const validate = vi.fn().mockResolvedValue({
        valid: true,
        wouldImport: true,
        summary: { total: 3, valid: 3, invalid: 0 },
        results: [],
      });
      mount({ import: vi.fn() }, { validate });

      await choose(user, goodFile);
      await user.click(screen.getByRole('button', { name: 'Check without writing' }));
      await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument());

      await user.click(screen.getByRole('button', { name: 'Clear' }));
      expect(screen.queryByText(/would register/)).not.toBeInTheDocument();
    });
  });
});
