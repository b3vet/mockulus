// SPDX-License-Identifier: Apache-2.0
import type { NearMissList } from '@mockulus/admin-sdk';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import NearMisses from './NearMisses.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi, type Api } from '../lib/api.svelte';
import {
  candidateMapping,
  journalClient,
  loggedRequest,
  nearMiss,
  type FakeJournalClientParts,
} from '../lib/journal-testing';
import { offerDraft, takeDraft } from '../lib/near-miss-handoff';
import { createRouter } from '../lib/router.svelte';
import { adminError } from '../lib/testing';

function mount(parts: FakeJournalClientParts): { api: Api } {
  window.history.pushState({}, '', '/near-misses');
  const client = journalClient(parts);
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/near-misses' }, { path: '/stubs/:id' }]);
  render(TestHost, { api, router, view: NearMisses });
  return { api };
}

function list(nearMisses: NearMissList['nearMisses']): NearMissList {
  return { nearMisses };
}

/** The refusal the journal-backed mode gets on a deployment nobody configured. */
function journalDisabled() {
  return vi.fn().mockRejectedValue(
    adminError(
      500,
      [
        {
          code: 1010,
          title: 'Journal disabled',
          detail: 'the request journal is disabled; set journal_enabled to record and verify',
        },
      ],
      { path: '/__admin/requests/unmatched/near-misses' },
    ),
  );
}

/**
 * Types a literal. `{` and `[` open a key descriptor in what user-event types,
 * and doubling them is how it spells the character itself — which a JSON body
 * needs on every line.
 */
function typed(value: string): string {
  return value.replace(/[{[]/g, '$&$&');
}

/** Fills the compose form and submits it. */
async function compose(
  user: ReturnType<typeof userEvent.setup>,
  values: { method?: string; url?: string; headers?: string; body?: string } = {},
) {
  if (values.method !== undefined) {
    await user.clear(screen.getByLabelText('Method'));
    await user.type(screen.getByLabelText('Method'), values.method);
  }
  if (values.url !== undefined) {
    await user.clear(screen.getByLabelText('URL'));
    await user.type(screen.getByLabelText('URL'), values.url);
  }
  if (values.headers !== undefined) {
    await user.type(screen.getByLabelText('Headers'), typed(values.headers));
  }
  if (values.body !== undefined) {
    await user.type(screen.getByLabelText('Body'), typed(values.body));
  }
  await user.click(screen.getByRole('button', { name: 'Find the closest stubs' }));
}

describe('NearMisses', () => {
  beforeEach(() => {
    sessionStorage.clear();
    takeDraft();
  });

  describe('compose mode, which is the one that needs no journal', () => {
    it('opens on it, and asks the journal nothing until its tab is opened', async () => {
      const unmatchedNearMisses = journalDisabled();
      mount({ nearMisses: { forRequest: vi.fn() }, requests: { unmatchedNearMisses } });

      expect(screen.getByRole('tab', { name: 'Compose a request' })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      // The journal is off by default, so this read would be a guaranteed 1010
      // spent on a tab nobody opened.
      expect(unmatchedNearMisses).not.toHaveBeenCalled();
      expect(screen.getByRole('form', { name: 'Describe a request' })).toBeInTheDocument();
    });

    it('sends the request the form describes, as a request rather than a pattern', async () => {
      const user = userEvent.setup();
      const forRequest = vi.fn().mockResolvedValue(list([]));
      mount({ nearMisses: { forRequest } });

      await compose(user, {
        method: 'post',
        url: '/api/orders?dryRun=true',
        headers: 'Content-Type: application/json',
        body: '{"id":1}',
      });

      expect(forRequest).toHaveBeenCalledWith({
        method: 'POST',
        url: '/api/orders?dryRun=true',
        headers: { 'Content-Type': 'application/json' },
        body: '{"id":1}',
      });
    });

    it('renders the differences per candidate as data, not as a document dump', async () => {
      const user = userEvent.setup();
      const request = loggedRequest({ method: 'GET', url: '/api/orders' });
      const forRequest = vi.fn().mockResolvedValue(
        list([
          nearMiss({
            request,
            stubMapping: candidateMapping(1, {
              name: 'orders — happy path',
              request: { method: 'POST', urlPath: '/api/orders' },
            }),
            matchResult: {
              distance: 0.2,
              differences: [
                { kind: 'method', expected: 'POST', actual: 'GET' },
                { kind: 'header', name: 'Accept', expected: 'application/json', actual: 'text/*' },
              ],
            },
          }),
        ]),
      );
      mount({ nearMisses: { forRequest } });

      await compose(user, { url: '/api/orders' });

      const table = await screen.findByRole('table');
      expect(within(table).getByRole('rowheader', { name: 'method' })).toBeInTheDocument();
      expect(within(table).getByRole('rowheader', { name: 'header Accept' })).toBeInTheDocument();
      expect(within(table).getByText('application/json')).toBeInTheDocument();
      expect(within(table).getByText('text/*')).toBeInTheDocument();
      expect(screen.getByText('80% match', { exact: false })).toBeInTheDocument();
      expect(screen.getByText('orders — happy path')).toBeInTheDocument();
    });

    it('links each candidate to the stub it is about', async () => {
      const user = userEvent.setup();
      const forRequest = vi
        .fn()
        .mockResolvedValue(list([nearMiss({ stubMapping: candidateMapping(3) })]));
      mount({ nearMisses: { forRequest } });

      await compose(user, { url: '/api/orders' });

      expect(await screen.findByRole('link', { name: 'Open this stub' })).toHaveAttribute(
        'href',
        '/stubs/00000000-0000-4000-8000-000000000003',
      );
    });

    it('shows the server reading of the description, which is what was scored', async () => {
      const user = userEvent.setup();
      const forRequest = vi.fn().mockResolvedValue(
        list([
          nearMiss({
            // The server echoes its own reading, and the distances below are
            // computed against that rather than against what was typed.
            request: loggedRequest({ method: 'POST', url: '/api/orders?dryRun=true' }),
          }),
        ]),
      );
      mount({ nearMisses: { forRequest } });

      await compose(user, { method: 'POST', url: '/api/orders?dryRun=true' });

      expect(await screen.findByText('As the server read your description')).toBeInTheDocument();
      expect(screen.getByText('POST /api/orders?dryRun=true')).toBeInTheDocument();
    });

    it('says so plainly when nothing in the snapshot resembles the request', async () => {
      const user = userEvent.setup();
      const forRequest = vi.fn().mockResolvedValue(list([]));
      mount({ nearMisses: { forRequest } });

      await compose(user, { url: '/api/orders' });

      expect(
        await screen.findByRole('heading', { name: 'No stub came close enough to report' }),
      ).toBeInTheDocument();
    });

    it('refuses a header line it cannot read rather than spending a round trip on it', async () => {
      const user = userEvent.setup();
      const forRequest = vi.fn().mockResolvedValue(list([]));
      mount({ nearMisses: { forRequest } });

      await compose(user, { url: '/api/orders', headers: 'Content-Type' });

      expect(screen.getByText(/Line 1 of the headers/)).toBeInTheDocument();
      expect(forRequest).not.toHaveBeenCalled();
    });

    it('escapes what the server echoes back, since it is text somebody supplied', async () => {
      const user = userEvent.setup();
      const forRequest = vi.fn().mockResolvedValue(
        list([
          nearMiss({
            matchResult: {
              distance: 0.5,
              differences: [
                { kind: 'body', expected: 'anything', actual: '<img src=x onerror="alert(1)">' },
              ],
            },
          }),
        ]),
      );
      mount({ nearMisses: { forRequest } });

      await compose(user, { url: '/api/orders' });

      expect(await screen.findByText('<img src=x onerror="alert(1)">')).toBeInTheDocument();
      expect(document.querySelector('img')).toBeNull();
    });

    it('asks for a token when the deployment refuses the scoring', async () => {
      const user = userEvent.setup();
      const forRequest = vi
        .fn()
        .mockRejectedValue(adminError(401, [{ code: 10, title: 'Unauthorized' }]));
      const { api } = mount({ nearMisses: { forRequest } });

      await compose(user, { url: '/api/orders' });

      expect(
        await screen.findByRole('heading', { name: 'This deployment needs an admin token' }),
      ).toBeInTheDocument();
      expect(api.tokenRequested).toBe(true);
    });
  });

  describe('carrying a journal entry across', () => {
    it('opens with the entry filled in, and says where it came from', () => {
      offerDraft({
        method: 'POST',
        url: '/api/orders',
        headers: 'Accept: application/json',
        cookies: '',
        body: '{"id":1}',
      });
      mount({ nearMisses: { forRequest: vi.fn() } });

      expect(screen.getByLabelText('URL')).toHaveValue('/api/orders');
      expect(screen.getByLabelText('Method')).toHaveValue('POST');
      expect(screen.getByLabelText('Headers')).toHaveValue('Accept: application/json');
      expect(screen.getByText(/Filled in from the journal entry/)).toBeInTheDocument();
    });

    it('takes the entry once, so a later visit starts blank', () => {
      offerDraft({
        method: 'POST',
        url: '/api/orders',
        headers: '',
        cookies: '',
        body: '',
      });
      mount({ nearMisses: { forRequest: vi.fn() } });
      expect(screen.getByLabelText('URL')).toHaveValue('/api/orders');

      // What a second visit in the same page load gets.
      expect(takeDraft()).toBeUndefined();
    });
  });

  describe('journal mode', () => {
    it('reads the unmatched entries once its tab is opened', async () => {
      const user = userEvent.setup();
      const unmatchedNearMisses = vi.fn().mockResolvedValue(
        list([
          nearMiss({
            request: loggedRequest({ method: 'PUT', url: '/api/orders/9' }),
            stubMapping: candidateMapping(9),
          }),
        ]),
      );
      mount({ nearMisses: { forRequest: vi.fn() }, requests: { unmatchedNearMisses } });

      await user.click(screen.getByRole('tab', { name: 'From the journal' }));

      expect(unmatchedNearMisses).toHaveBeenCalledTimes(1);
      expect(await screen.findByText('Recorded request that matched nothing')).toBeInTheDocument();
      expect(screen.getByText('PUT /api/orders/9')).toBeInTheDocument();
    });

    it('reads 1010 as configuration, offers no retry, and points at the mode that works', async () => {
      const user = userEvent.setup();
      mount({
        nearMisses: { forRequest: vi.fn() },
        requests: { unmatchedNearMisses: journalDisabled() },
      });

      await user.click(screen.getByRole('tab', { name: 'From the journal' }));

      expect(
        await screen.findByRole('heading', { name: 'The request journal is off' }),
      ).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: 'Describe a request instead' }));

      expect(screen.getByRole('tab', { name: 'Compose a request' })).toHaveAttribute(
        'aria-selected',
        'true',
      );
    });

    it('tells a journal with nothing unmatched apart from one that is off', async () => {
      const user = userEvent.setup();
      mount({
        nearMisses: { forRequest: vi.fn() },
        requests: { unmatchedNearMisses: vi.fn().mockResolvedValue(list([])) },
      });

      await user.click(screen.getByRole('tab', { name: 'From the journal' }));

      expect(
        await screen.findByRole('heading', { name: 'Nothing recorded went unmatched' }),
      ).toBeInTheDocument();
    });

    it('keeps what it found when the reader goes back and forth', async () => {
      const user = userEvent.setup();
      const unmatchedNearMisses = vi.fn().mockResolvedValue(list([nearMiss()]));
      mount({ nearMisses: { forRequest: vi.fn() }, requests: { unmatchedNearMisses } });

      await user.click(screen.getByRole('tab', { name: 'From the journal' }));
      await screen.findByText('Recorded request that matched nothing');
      await user.click(screen.getByRole('tab', { name: 'Compose a request' }));
      await user.click(screen.getByRole('tab', { name: 'From the journal' }));

      // Re-mounting the mode would spend the read again and throw away what the
      // reader was looking at.
      expect(unmatchedNearMisses).toHaveBeenCalledTimes(1);
    });
  });

  it('moves between the modes with the arrow keys, as a tab list does', async () => {
    const user = userEvent.setup();
    mount({
      nearMisses: { forRequest: vi.fn() },
      requests: { unmatchedNearMisses: vi.fn().mockResolvedValue(list([])) },
    });

    await user.click(screen.getByRole('tab', { name: 'Compose a request' }));
    await user.keyboard('{ArrowRight}');

    expect(screen.getByRole('tab', { name: 'From the journal' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByRole('tab', { name: 'From the journal' })).toHaveFocus();
  });
});
