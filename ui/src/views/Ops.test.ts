// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, StubMapping } from '@mockulus/admin-sdk';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import Ops from './Ops.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi } from '../lib/api.svelte';
import { createRouter } from '../lib/router.svelte';
import { adminError, fakeClient, health, stubMapping, versionInfo } from '../lib/testing';

interface Parts {
  system?: Partial<MockulusClient['system']>;
  settings?: Partial<MockulusClient['settings']>;
  files?: Partial<MockulusClient['files']>;
  mappings?: Partial<MockulusClient['mappings']>;
  requests?: Partial<MockulusClient['requests']>;
}

function paginate(mappings: readonly StubMapping[]) {
  return (async function* () {
    for (const mapping of mappings) {
      yield mapping;
    }
  })();
}

function mount(parts: Parts = {}) {
  window.history.pushState({}, '', '/ops');
  const client = fakeClient({
    system: {
      health: () => Promise.resolve(health()),
      version: () => Promise.resolve(versionInfo()),
      ...parts.system,
    },
    settings: { get: () => Promise.resolve({ settings: {} }), ...parts.settings },
    files: { list: () => Promise.resolve([]), ...parts.files },
    mappings: { paginate: () => paginate([]), ...parts.mappings },
    requests: { ...parts.requests },
  });
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/ops' }]);
  render(TestHost, { api, router, view: Ops });
  return { api, router };
}

describe('Ops overview', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('reports the snapshot this replica holds, and the surface claim that identifies the server', async () => {
    mount({
      system: {
        health: () =>
          Promise.resolve(health({ stubs: 42, epoch: 9, store: { driver: 'couchbase' } })),
      },
    });

    expect(await screen.findByText('couchbase')).toBeInTheDocument();
    expect(screen.getByText('42')).toBeInTheDocument();
    expect(screen.getByText('9')).toBeInTheDocument();
    // Reachability is not identity: the surface claim is what says a mockulus
    // is on the other end rather than whatever else took the port.
    expect(screen.getByText('3.x-subset')).toBeInTheDocument();
  });

  it('says where the quarantine count lives rather than inventing one', async () => {
    // It is a Prometheus counter on /metrics, outside /__admin, so it is
    // outside the SDK this UI is required to talk through.
    mount();

    expect(await screen.findByText(/mockulus_snapshot_quarantined_total/)).toBeInTheDocument();
  });
});

describe('Ops files', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('links each stored file back to the stubs whose bodyFileName names it', async () => {
    mount({
      files: { list: () => Promise.resolve(['order.json', 'unused.bin']) },
      mappings: {
        paginate: () =>
          paginate([
            stubMapping(1, { id: 'the-id', response: { status: 200, bodyFileName: 'order.json' } }),
            stubMapping(2),
          ]),
      },
    });

    // Awaited rather than read off the first render: the file listing and the
    // mappings are two calls, and the back-links only exist once the second
    // has answered.
    expect(await screen.findByText('referenced by 1 stub')).toBeInTheDocument();
    const list = screen.getByRole('list', { name: 'Stored files' });
    expect(within(list).getByText('referenced by no stub')).toBeInTheDocument();
    expect(within(list).getByRole('link', { name: '/api/things/1' })).toHaveAttribute(
      'href',
      '/stubs/the-id',
    );
  });

  it('names the references that point at a file the store does not hold', async () => {
    mount({
      files: { list: () => Promise.resolve([]) },
      mappings: {
        paginate: () =>
          paginate([stubMapping(1, { response: { status: 200, bodyFileName: 'gone.json' } })]),
      },
    });

    expect(
      await screen.findByRole('heading', {
        name: /1 reference names a file the store does not hold/,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText(/code 1022/)).toBeInTheDocument();
  });

  it('uploads the chosen bytes verbatim under the name that was typed', async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockResolvedValue(undefined);
    mount({ files: { list: () => Promise.resolve([]), put } });
    await screen.findByRole('heading', { name: 'Response-body files' });

    // Bytes that are not valid UTF-8, because a response body is as likely to
    // be a PNG as it is to be JSON and a text round trip would corrupt it.
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe]);
    await user.upload(
      screen.getByLabelText('Choose a file…'),
      new File([bytes], 'logo.png', { type: 'image/png' }),
    );
    await user.click(screen.getByRole('button', { name: 'Upload' }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [name, body, options] = put.mock.calls[0] as [
      string,
      ArrayBuffer,
      { contentType: string },
    ];
    expect(name).toBe('logo.png');
    expect(options.contentType).toBe('image/png');
    expect([...new Uint8Array(body)]).toEqual([...bytes]);
  });

  it('names the stubs that will start answering 1022 before a file is deleted', async () => {
    const user = userEvent.setup();
    const remove = vi.fn().mockResolvedValue(undefined);
    mount({
      files: { list: () => Promise.resolve(['order.json']), delete: remove },
      mappings: {
        paginate: () =>
          paginate([stubMapping(1, { response: { status: 200, bodyFileName: 'order.json' } })]),
      },
    });
    await screen.findByRole('list', { name: 'Stored files' });

    await user.click(screen.getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/1 stub references it/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    expect(remove).not.toHaveBeenCalled();
  });
});

describe('Ops settings', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('sends the whole document, because the endpoint replaces rather than merges', async () => {
    const user = userEvent.setup();
    const update = vi.fn().mockResolvedValue(undefined);
    mount({
      settings: {
        get: () =>
          Promise.resolve({
            settings: {
              fixedDelay: 20,
              delayDistribution: { type: 'uniform', lower: 5, upper: 9 },
            },
          }),
        update,
      },
    });
    const fixed = await screen.findByLabelText('Fixed delay (milliseconds)');

    await user.clear(fixed);
    await user.type(fixed, '30');
    await user.click(screen.getByRole('button', { name: 'Save settings' }));

    // The distribution nobody touched has to travel with the edit; sending only
    // the changed field would clear it.
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith({
        fixedDelay: 30,
        delayDistribution: { type: 'uniform', lower: 5, upper: 9 },
      }),
    );
  });

  it('refuses a delay that is not a number without calling the server', async () => {
    const user = userEvent.setup();
    const update = vi.fn().mockResolvedValue(undefined);
    mount({ settings: { update } });
    const fixed = await screen.findByLabelText('Fixed delay (milliseconds)');

    await user.type(fixed, 'soon');
    await user.click(screen.getByRole('button', { name: 'Save settings' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('whole number of milliseconds');
    expect(update).not.toHaveBeenCalled();
  });
});

describe('Ops danger zone', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('holds the confirm button dead until the phrase for that action is typed', async () => {
    const user = userEvent.setup();
    const resetAll = vi.fn().mockResolvedValue(undefined);
    mount({ system: { resetAll } });
    await screen.findByRole('heading', { name: 'Danger zone' });

    await user.click(screen.getByRole('button', { name: 'Reset the whole deployment' }));

    const dialog = await screen.findByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'Reset the whole deployment' });
    expect(confirm).toBeDisabled();
    // Focus is on the field rather than on Cancel: the reflex Enter is already
    // harmless while the button is dead, and the work is in the box.
    expect(within(dialog).getByLabelText(/Type/)).toHaveFocus();

    // Another action's phrase must never unlock this one.
    await user.type(within(dialog).getByLabelText(/Type/), 'clear the journal');
    expect(confirm).toBeDisabled();

    await user.clear(within(dialog).getByLabelText(/Type/));
    await user.type(within(dialog).getByLabelText(/Type/), 'reset the whole deployment');
    expect(confirm).toBeEnabled();

    await user.click(confirm);

    await waitFor(() => expect(resetAll).toHaveBeenCalledTimes(1));
  });

  it('says what each call destroys and what it leaves alone, before it is pressed', async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole('heading', { name: 'Danger zone' });

    await user.click(screen.getByRole('button', { name: 'Reset the mappings' }));

    const dialog = await screen.findByRole('dialog');
    expect(
      within(dialog).getByText(/This destroys, for every caller of this deployment/),
    ).toBeInTheDocument();
    expect(within(dialog).getByText('It leaves alone:')).toBeInTheDocument();
    expect(within(dialog).getByText(/The request journal\./)).toBeInTheDocument();
  });

  it('forgets a phrase typed into a dialog that was dismissed', async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole('heading', { name: 'Danger zone' });

    await user.click(screen.getByRole('button', { name: 'Clear the journal' }));
    await user.type(
      within(await screen.findByRole('dialog')).getByLabelText(/Type/),
      'clear the journal',
    );
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    // The modal holds `pointer-events: none` on the body while it is open and
    // releases it a tick after closing, so a click issued too early is refused
    // by the pointer-events check rather than by the page.
    await waitFor(() => expect(document.body.style.pointerEvents).toBe(''));

    await user.click(screen.getByRole('button', { name: 'Clear the journal' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByLabelText(/Type/)).toHaveValue('');
    expect(within(dialog).getByRole('button', { name: 'Clear the journal' })).toBeDisabled();
  });

  it('returns focus to the button that opened the dialog', async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole('heading', { name: 'Danger zone' });

    const trigger = screen.getByRole('button', { name: 'Clear the journal' });
    await user.click(trigger);
    await screen.findByRole('dialog');

    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});

describe('Ops during a store outage', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('says what still works rather than only what broke', async () => {
    const outage = () =>
      Promise.reject(adminError(503, [{ code: 1020, title: 'Store unavailable' }]));
    mount({ settings: { get: outage }, files: { list: outage } });

    expect(
      await screen.findByRole('heading', {
        name: 'The stub store is unavailable — this deployment is degraded, not down',
      }),
    ).toBeInTheDocument();
    expect(screen.getByText(/Mock traffic is still being served/)).toBeInTheDocument();
    // The overview reads the snapshot rather than the store, so it keeps
    // answering — which is the whole point of the degraded mode.
    expect(screen.getByText('memory')).toBeInTheDocument();
  });

  it('shows no outage banner when the store is answering', async () => {
    mount();

    await screen.findByRole('heading', { name: 'Overview' });

    expect(screen.queryByText(/degraded, not down/)).not.toBeInTheDocument();
  });
});
