// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, ServeEvent, ServeEventList } from '@mockulus/admin-sdk';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Journal from './Journal.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi, type Api } from '../lib/api.svelte';
import { AUTO_REFRESH_INTERVAL_MS } from '../lib/journal-entries';
import { journalClient, loggedRequest, serveEvent } from '../lib/journal-testing';
import { takeDraft } from '../lib/near-miss-handoff';
import { createRouter } from '../lib/router.svelte';
import { adminError } from '../lib/testing';

/** The envelope `GET /__admin/requests` answers with, over these entries. */
function serveEventList(events: readonly ServeEvent[], total = events.length): ServeEventList {
  return { requests: [...events], meta: { total }, requestJournalDisabled: false };
}

function mount(list: MockulusClient['requests']['list']): {
  api: Api;
  unmount: () => void;
} {
  window.history.pushState({}, '', '/journal');
  const client = journalClient({ requests: { list } });
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/journal' }, { path: '/near-misses' }]);
  const { unmount } = render(TestHost, { api, router, view: Journal });
  return { api, unmount };
}

/** A client that answers every read with these entries. */
function listing(events: readonly ServeEvent[], total?: number) {
  return vi.fn().mockResolvedValue(serveEventList(events, total));
}

/** The refusal a deployment that has never been configured answers with. */
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
      { path: '/__admin/requests' },
    ),
  );
}

const rows = () =>
  within(screen.getByRole('list', { name: 'Journal entries' })).getAllByRole('button');

/**
 * A literal, as a pattern. Totals are grouped for the reader's locale, so the
 * expectation is built the same way rather than pinned to one separator — and a
 * separator that is a regular-expression metacharacter has to survive the trip.
 */
function literal(value: string): RegExp {
  return new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
}

describe('Journal', () => {
  beforeEach(() => {
    sessionStorage.clear();
    // The handoff slot is module state shared by every test in the process.
    takeDraft();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe('with the journal off, which is the default deployment', () => {
    it('reads 1010 as configuration and says what turns recording on', async () => {
      mount(journalDisabled());

      expect(
        await screen.findByRole('heading', { name: 'The request journal is off' }),
      ).toBeInTheDocument();
      expect(screen.getByText('journal_enabled: true')).toBeInTheDocument();
      expect(screen.getByText('MOCKULUS_JOURNAL_ENABLED=true')).toBeInTheDocument();
    });

    it('offers no retry, because nothing about pressing it would help', async () => {
      mount(journalDisabled());

      await screen.findByRole('heading', { name: 'The request journal is off' });

      expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
    });

    it('withdraws the controls that would window and poll a journal there is none of', async () => {
      mount(journalDisabled());

      await screen.findByRole('heading', { name: 'The request journal is off' });

      expect(screen.queryByLabelText('Entries to read')).not.toBeInTheDocument();
      expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
      expect(screen.queryByRole('checkbox', { name: /Re-read every/ })).not.toBeInTheDocument();
    });

    it('sends the reader to the debugging that does work without a journal', async () => {
      mount(journalDisabled());

      await screen.findByRole('heading', { name: 'The request journal is off' });

      expect(screen.getByRole('link', { name: 'Open the near-miss debugger' })).toHaveAttribute(
        'href',
        '/near-misses',
      );
    });

    it('never starts a poll against it', async () => {
      vi.useFakeTimers();
      const list = journalDisabled();
      mount(list);

      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_INTERVAL_MS * 4);

      // One read, the one that discovered the configuration. A timer re-asking
      // a question whose answer is a config file is the clearest way to teach an
      // operator that a correct deployment looks broken.
      expect(list).toHaveBeenCalledTimes(1);
    });
  });

  describe('with the journal on', () => {
    it('lists what the replica served, with its outcome and its status', async () => {
      mount(listing([serveEvent(1), serveEvent(2, { wasMatched: false })]));

      await screen.findByRole('list', { name: 'Journal entries' });

      expect(rows()).toHaveLength(2);
      expect(screen.getByText('/api/orders/1')).toBeInTheDocument();
      expect(screen.getByText(/200 · matched/)).toBeInTheDocument();
      expect(screen.getByText(/404 · unmatched/)).toBeInTheDocument();
    });

    it('tells an empty journal apart from one that is off', async () => {
      mount(listing([]));

      expect(
        await screen.findByRole('heading', { name: 'The journal is on and holds nothing here' }),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole('heading', { name: 'The request journal is off' }),
      ).not.toBeInTheDocument();
    });

    it('splits the window by outcome behind real tabs', async () => {
      const user = userEvent.setup();
      mount(listing([serveEvent(1), serveEvent(2, { wasMatched: false }), serveEvent(3)]));

      await screen.findByRole('list', { name: 'Journal entries' });
      expect(rows()).toHaveLength(3);

      await user.click(screen.getByRole('tab', { name: /Unmatched/ }));

      expect(rows()).toHaveLength(1);
      expect(screen.getByRole('tab', { name: /Unmatched/ })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      expect(screen.getByRole('tabpanel')).toHaveAttribute(
        'aria-labelledby',
        'journal-tab-unmatched',
      );
    });

    it('counts each tab over the one window that was read', async () => {
      mount(listing([serveEvent(1), serveEvent(2, { wasMatched: false }), serveEvent(3)]));

      await screen.findByRole('list', { name: 'Journal entries' });

      expect(screen.getByRole('tab', { name: 'All 3' })).toBeInTheDocument();
      expect(screen.getByRole('tab', { name: 'Matched 2' })).toBeInTheDocument();
      expect(screen.getByRole('tab', { name: 'Unmatched 1' })).toBeInTheDocument();
    });

    it('moves between tabs with the arrow keys, as a tab list does', async () => {
      const user = userEvent.setup();
      mount(listing([serveEvent(1)]));

      await screen.findByRole('list', { name: 'Journal entries' });
      await user.click(screen.getByRole('tab', { name: 'All 1' }));
      await user.keyboard('{ArrowRight}');

      expect(screen.getByRole('tab', { name: 'Matched 1' })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      expect(screen.getByRole('tab', { name: 'Matched 1' })).toHaveFocus();
    });

    it('opens an entry onto what was served and links the stub that served it', async () => {
      const user = userEvent.setup();
      mount(
        listing([
          serveEvent(1, {
            request: loggedRequest({
              url: '/api/orders/1',
              headers: { Accept: 'application/json' },
              body: '{"id":1}',
            }),
            stubMapping: { id: 'stub-7', name: 'orders — happy path' },
          }),
        ]),
      );

      await screen.findByRole('list', { name: 'Journal entries' });
      const row = rows()[0];
      expect(row).toHaveAttribute('aria-expanded', 'false');

      await user.click(row as HTMLElement);

      expect(row).toHaveAttribute('aria-expanded', 'true');
      expect(screen.getByText('orders — happy path', { exact: false })).toBeInTheDocument();
      expect(screen.getByRole('link', { name: 'Open this stub' })).toHaveAttribute(
        'href',
        '/stubs/stub-7',
      );
      expect(screen.getByText('Accept')).toBeInTheDocument();
      expect(screen.getByText('{"id":1}')).toBeInTheDocument();
    });

    it('renders a request body as text, never as markup', async () => {
      const user = userEvent.setup();
      const body = '<img src=x onerror="alert(1)">';
      mount(listing([serveEvent(1, { request: loggedRequest({ body }) })]));

      await screen.findByRole('list', { name: 'Journal entries' });
      await user.click(rows()[0] as HTMLElement);

      // The journal carries whatever drove the mock. Finding the text means it
      // was escaped; an element would mean it was parsed.
      expect(screen.getByText(body)).toBeInTheDocument();
      expect(document.querySelector('img')).toBeNull();
    });

    it('hands an unmatched entry to the debugger and goes there', async () => {
      const user = userEvent.setup();
      mount(
        listing([
          serveEvent(1, {
            wasMatched: false,
            request: loggedRequest({
              method: 'POST',
              url: '/api/orders?dryRun=true',
              headers: { Accept: 'application/json' },
              body: '{"id":1}',
            }),
          }),
        ]),
      );

      await screen.findByRole('list', { name: 'Journal entries' });
      await user.click(rows()[0] as HTMLElement);
      await user.click(screen.getByRole('button', { name: 'Find out why nothing matched' }));

      expect(window.location.pathname).toBe('/near-misses');
      expect(takeDraft()).toEqual({
        method: 'POST',
        url: '/api/orders?dryRun=true',
        headers: 'Accept: application/json',
        cookies: '',
        body: '{"id":1}',
      });
    });

    it('re-reads with the limit the reader chose', async () => {
      const user = userEvent.setup();
      const list = listing([serveEvent(1)]);
      mount(list);

      await screen.findByRole('list', { name: 'Journal entries' });
      await user.selectOptions(screen.getByLabelText('Entries to read'), 'Newest 500');

      expect(list).toHaveBeenLastCalledWith({ limit: 500, since: undefined });
    });

    it('bounds the read at the window the reader chose, resolved at the read', async () => {
      const user = userEvent.setup();
      const list = listing([serveEvent(1)]);
      mount(list);

      await screen.findByRole('list', { name: 'Journal entries' });
      await user.selectOptions(screen.getByLabelText('Recorded within'), 'The last 15 minutes');

      const since = list.mock.lastCall?.[0]?.since as Date | undefined;
      expect(since).toBeInstanceOf(Date);
      // Fifteen minutes before now, not before whenever the control was
      // touched: an auto-refreshing page whose bound froze would slowly stop
      // showing the traffic it was opened to watch.
      const minutesBack = (Date.now() - (since?.getTime() ?? 0)) / 60_000;
      expect(minutesBack).toBeGreaterThan(14.9);
      expect(minutesBack).toBeLessThan(15.1);
    });

    it('says how much of the window it is showing, and that nothing pages past it', async () => {
      mount(listing([serveEvent(1)], 4210));

      await screen.findByRole('list', { name: 'Journal entries' });

      expect(
        screen.getByText(literal(`The journal holds ${(4210).toLocaleString()} in this window`)),
      ).toBeInTheDocument();
      expect(screen.getByText(/The journal has no offset/)).toBeInTheDocument();
    });

    it('announces each read in a live region', async () => {
      mount(listing([serveEvent(1), serveEvent(2, { wasMatched: false })]));

      const announcement = await screen.findByText(/Journal read at .*2 entries, 1 unmatched\./);
      expect(announcement).toHaveAttribute('role', 'status');
    });

    it('re-reads on a timer once auto-refresh is on', async () => {
      vi.useFakeTimers();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const list = listing([serveEvent(1)]);
      mount(list);

      await vi.advanceTimersByTimeAsync(0);
      await user.click(screen.getByRole('checkbox', { name: /Re-read every/ }));
      expect(list).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_INTERVAL_MS);
      expect(list).toHaveBeenCalledTimes(2);

      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_INTERVAL_MS);
      expect(list).toHaveBeenCalledTimes(3);
    });

    it('stops the timer when the view is destroyed', async () => {
      vi.useFakeTimers();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const list = listing([serveEvent(1)]);
      const { unmount } = mount(list);

      await vi.advanceTimersByTimeAsync(0);
      await user.click(screen.getByRole('checkbox', { name: /Re-read every/ }));
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_INTERVAL_MS);
      expect(list).toHaveBeenCalledTimes(2);

      unmount();
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_INTERVAL_MS * 5);

      // A timer that outlived its page would keep re-reading a journal nobody
      // is looking at for as long as the tab is open.
      expect(list).toHaveBeenCalledTimes(2);
    });

    it('asks for a token when the deployment refuses the read', async () => {
      const { api } = mount(
        vi.fn().mockRejectedValue(adminError(401, [{ code: 10, title: 'Unauthorized' }])),
      );

      expect(
        await screen.findByRole('heading', { name: 'This deployment needs an admin token' }),
      ).toBeInTheDocument();
      expect(api.tokenRequested).toBe(true);
    });
  });
});
