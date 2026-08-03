// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import StubEditor from './StubEditor.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi, type Api } from '../lib/api.svelte';
import { createRouter } from '../lib/router.svelte';
import { adminError, fakeClient, stubMapping } from '../lib/testing';

/** A stub whose request carries a regex, so a pointer into an array has something to name. */
const SOURCE: StubMapping = stubMapping(1, {
  request: {
    method: 'GET',
    urlPath: '/api/orders',
    bodyPatterns: [{ matches: '(' }],
  },
});

function mount(path: string, mappings: Partial<MockulusClient['mappings']>): { api: Api } {
  window.history.pushState({}, '', path);
  const client = fakeClient({ mappings });
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([
    { path: '/stubs/new' },
    { path: '/stubs/:id/edit' },
    { path: '/stubs/:id/duplicate' },
  ]);
  render(TestHost, { api, router, view: StubEditor });
  return { api };
}

/** The editor's editable region, once the draft has been seeded into it. */
const editor = () => screen.findByRole('textbox', { name: 'Stub mapping JSON' });

/** The document as the editor holds it, with CodeMirror's line boxes flattened away. */
function documentText(region: HTMLElement): string {
  return [...region.querySelectorAll('.cm-line')].map((line) => line.textContent).join('\n');
}

describe('StubEditor', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('opens a new stub on a document that would register as it stands', async () => {
    mount('/stubs/new', {});

    const region = await editor();
    const draft: unknown = JSON.parse(documentText(region));

    expect(draft).toEqual({
      request: { method: 'GET', urlPath: '/example' },
      response: {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
        jsonBody: { hello: 'world' },
      },
    });
  });

  it('creates the stub and lands on it, so the write is confirmed by the thing itself', async () => {
    const user = userEvent.setup();
    const create = vi
      .fn<(mapping: StubMapping) => Promise<StubMapping>>()
      .mockImplementation((mapping) => Promise.resolve({ ...mapping, id: 'written-id' }));
    mount('/stubs/new', { create } as Partial<MockulusClient['mappings']>);
    await editor();

    await user.click(screen.getByRole('button', { name: 'Create stub' }));

    await waitFor(() => expect(window.location.pathname).toBe('/stubs/written-id'));
    expect(create).toHaveBeenCalledTimes(1);
    expect(create.mock.calls[0]?.[0]).toEqual({
      request: { method: 'GET', urlPath: '/example' },
      response: {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
        jsonBody: { hello: 'world' },
      },
    });
  });

  it('edits the document as stored and replaces the stub named by the path', async () => {
    const user = userEvent.setup();
    const getOrNull = vi.fn().mockResolvedValue(SOURCE);
    const update = vi
      .fn<(id: string, mapping: StubMapping) => Promise<StubMapping>>()
      .mockImplementation((id, mapping) => Promise.resolve({ ...mapping, id }));
    mount('/stubs/the-id/edit', { getOrNull, update } as Partial<MockulusClient['mappings']>);

    const region = await editor();
    // Identity included: this is what the detail page showed and what the round
    // trip carries, and hiding it would make the editor a different document.
    expect(documentText(region)).toContain(SOURCE.id ?? '');

    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
    // The path names the stub being replaced, not the `id` in the body.
    expect(update.mock.calls[0]?.[0]).toBe('the-id');
    expect(update.mock.calls[0]?.[1]).toEqual(SOURCE);
  });

  it('opens a duplicate as an unwritten draft with no identity in it', async () => {
    const user = userEvent.setup();
    const getOrNull = vi.fn().mockResolvedValue(SOURCE);
    const create = vi
      .fn<(mapping: StubMapping) => Promise<StubMapping>>()
      .mockImplementation((mapping) => Promise.resolve({ ...mapping, id: 'copy-id' }));
    mount('/stubs/the-id/duplicate', { getOrNull, create } as Partial<MockulusClient['mappings']>);

    const region = await editor();
    // Nothing has been written yet: arriving here is opening a draft.
    expect(create).not.toHaveBeenCalled();
    expect(documentText(region)).not.toContain(SOURCE.id ?? '');

    await user.click(screen.getByRole('button', { name: 'Create the copy' }));

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    const written = create.mock.calls[0]?.[0];
    expect(written).not.toHaveProperty('id');
    expect(written?.name).toBe('stub 1 (copy)');
  });

  it('renders every problem in a 422 and takes the cursor to the field one names', async () => {
    const user = userEvent.setup();
    const getOrNull = vi.fn().mockResolvedValue(SOURCE);
    const update = vi.fn().mockRejectedValue(
      adminError(
        422,
        [
          {
            code: 1003,
            title: 'Invalid regular expression',
            detail: 'pattern does not compile: missing closing )',
            source: { pointer: '/request/bodyPatterns/0/matches' },
          },
          {
            code: 1000,
            title: 'Unsupported feature',
            detail: 'multipartPatterns is not supported in mockulus v1 — see ROADMAP.md',
            source: { pointer: '/request/multipartPatterns' },
          },
          {
            code: 10,
            title: 'Malformed request',
            detail: 'urlPath must not be empty',
            source: { pointer: '/request/urlPath' },
          },
        ],
        { method: 'PUT', path: '/__admin/mappings/the-id' },
      ),
    );
    mount('/stubs/the-id/edit', { getOrNull, update } as Partial<MockulusClient['mappings']>);
    const region = await editor();

    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    // All three, because the server validated the whole document before
    // answering; reporting one of three would waste the round trip the
    // collect-all envelope exists to save.
    const list = await screen.findByRole('list', { name: 'Problems the server reported' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(3);
    expect(
      screen.getByRole('heading', {
        name: 'The server refused this mapping, and nothing was written',
      }),
    ).toBeInTheDocument();

    // The one pointer that names nothing in this document says so rather than
    // offering a control that would do nothing.
    expect(
      screen.queryByRole('button', { name: 'Go to /request/multipartPatterns' }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/deepest part that does resolve is \/request/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Go to /request/bodyPatterns/0/matches' }));

    expect(region).toHaveFocus();
    expect(window.getSelection()?.toString()).toContain('"matches": "("');
  });

  it('does not send a document that is not JSON, and offers to show where it stops parsing', async () => {
    const user = userEvent.setup();
    const create = vi.fn();
    mount('/stubs/new', { create } as Partial<MockulusClient['mappings']>);
    const region = await editor();

    // One stray character at the head of the document. This also proves the
    // editor's text reaches the page: the parse below is of what was typed.
    await user.click(region);
    await user.keyboard('x');
    await user.click(screen.getByRole('button', { name: 'Create stub' }));

    expect(create).not.toHaveBeenCalled();
    expect(screen.getByRole('heading', { name: 'This document was not sent' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Go to where it stops parsing' }));
    expect(region).toHaveFocus();
  });

  it('opens the token sheet on a 401 and saves the same document once a token arrives', async () => {
    const user = userEvent.setup();
    let authorized = false;
    const written: StubMapping[] = [];
    const create = vi.fn().mockImplementation((mapping: StubMapping) => {
      if (!authorized) {
        return Promise.reject(adminError(401, [{ code: 10, title: 'Unauthorized' }]));
      }
      written.push(mapping);
      return Promise.resolve({ ...mapping, id: 'written-id' });
    });
    const { api } = mount('/stubs/new', { create } as Partial<MockulusClient['mappings']>);
    await editor();

    await user.click(screen.getByRole('button', { name: 'Create stub' }));
    await waitFor(() => expect(api.tokenRequested).toBe(true));
    expect(written).toEqual([]);

    authorized = true;
    api.submitToken('s3cret');

    // The draft is not lost by the refusal: the same document is sent again.
    await waitFor(() => expect(written).toHaveLength(1));
    expect(written[0]?.request?.urlPath).toBe('/example');
  });

  it('sends a store outage to the shared error state rather than to the pointer list', async () => {
    const user = userEvent.setup();
    const create = vi
      .fn()
      .mockRejectedValue(adminError(503, [{ code: 1020, title: 'Store unavailable' }]));
    mount('/stubs/new', { create } as Partial<MockulusClient['mappings']>);
    await editor();

    await user.click(screen.getByRole('button', { name: 'Create stub' }));

    expect(
      await screen.findByRole('heading', { name: 'The stub store is unavailable' }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('list', { name: 'Problems the server reported' }),
    ).not.toBeInTheDocument();
  });

  it('says so when the stub to edit no longer exists', async () => {
    mount('/stubs/gone/edit', {
      getOrNull: vi.fn().mockResolvedValue(null),
    } as Partial<MockulusClient['mappings']>);

    expect(
      await screen.findByRole('heading', { name: 'No stub with that id' }),
    ).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Stub mapping JSON' })).not.toBeInTheDocument();
  });
});
