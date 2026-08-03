// SPDX-License-Identifier: Apache-2.0
import type { MockulusClient, ScenarioList, StubMapping } from '@mockulus/admin-sdk';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import Scenarios from './Scenarios.svelte';
import TestHost from '../lib/TestHost.svelte';
import { createApi } from '../lib/api.svelte';
import { panelClient, scenario } from '../lib/ops-testing';
import { createRouter } from '../lib/router.svelte';
import { adminError, stubMapping } from '../lib/testing';

interface Parts {
  scenarios?: Partial<MockulusClient['scenarios']>;
  mappings?: Partial<MockulusClient['mappings']>;
}

function mount(parts: Parts) {
  window.history.pushState({}, '', '/scenarios');
  const client = panelClient({
    scenarios: { list: () => Promise.resolve({ scenarios: [] }), ...parts.scenarios },
    mappings: { paginate: () => paginate([]), ...parts.mappings },
  });
  const api = createApi({ baseUrl: 'http://mock.example', createClient: () => client });
  const router = createRouter([{ path: '/scenarios' }]);
  render(TestHost, { api, router, view: Scenarios });
  return { api, router };
}

/** The SDK's paginate is an async generator; a fake has to be one too. */
function paginate(mappings: readonly StubMapping[]) {
  return (async function* () {
    for (const mapping of mappings) {
      yield mapping;
    }
  })();
}

function listing(...scenarios: ScenarioList['scenarios']): () => Promise<ScenarioList> {
  return () => Promise.resolve({ scenarios });
}

describe('Scenarios', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('shows each scenario’s current state and offers the others as one click', async () => {
    mount({ scenarios: { list: listing(scenario('checkout', 'Ready', ['Ready', 'Done'])) } });

    expect(await screen.findByRole('heading', { name: 'checkout' })).toBeInTheDocument();
    // The state it is in is a marker, not a control: a disabled button in a row
    // of enabled ones is a tab stop that answers nothing.
    expect(screen.getByText('Ready · current')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Set checkout to Done' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Set checkout to Started' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Set checkout to Ready' })).not.toBeInTheDocument();
  });

  it('offers Started even where WireMock would refuse it, and sends it as a state write', async () => {
    // Deviation #34: Started is a possible state of every scenario here, so
    // setting it is always accepted. WireMock derives possibleStates from the
    // stubs alone and refuses Started when no stub names it.
    const user = userEvent.setup();
    const setState = vi.fn().mockResolvedValue(undefined);
    mount({
      scenarios: { list: listing(scenario('checkout', 'Ready', ['Ready', 'Done'])), setState },
    });
    await screen.findByRole('heading', { name: 'checkout' });

    await user.click(screen.getByRole('button', { name: 'Set checkout to Started' }));

    await waitFor(() => expect(setState).toHaveBeenCalledWith('checkout', 'Started'));
  });

  it('resets one scenario through the same write, and offers nothing to reset at the start', async () => {
    const user = userEvent.setup();
    const setState = vi.fn().mockResolvedValue(undefined);
    mount({
      scenarios: {
        list: listing(
          scenario('checkout', 'Done', ['Ready', 'Done']),
          scenario('login', 'Started', ['Ready']),
        ),
        setState,
      },
    });
    await screen.findByRole('heading', { name: 'checkout' });

    expect(screen.getByRole('button', { name: 'Reset login to Started' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Reset checkout to Started' }));

    await waitFor(() => expect(setState).toHaveBeenCalledWith('checkout', 'Started'));
  });

  it('re-reads the listing after a state write rather than patching the card', async () => {
    const user = userEvent.setup();
    let current = 'Ready';
    const list = vi.fn(() =>
      Promise.resolve({ scenarios: [scenario('checkout', current, ['Ready', 'Done'])] }),
    );
    const setState = vi.fn().mockImplementation((_name: string, state: string) => {
      current = state;
      return Promise.resolve();
    });
    mount({ scenarios: { list, setState } });
    await screen.findByText('Ready · current');

    await user.click(screen.getByRole('button', { name: 'Set checkout to Done' }));

    expect(await screen.findByText('Done · current')).toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(2);
  });

  it('does not read the mappings until somebody asks which stubs serve where', async () => {
    // GET /__admin/scenarios deliberately does not embed member stubs the way
    // WireMock does (deviation #32), so this is a second query — and one a page
    // of cards should not pay for on every visit.
    const user = userEvent.setup();
    const paginateSpy = vi.fn(() =>
      paginate([
        stubMapping(1, {
          scenarioName: 'checkout',
          requiredScenarioState: 'Ready',
          newScenarioState: 'Done',
        }),
        stubMapping(2, { scenarioName: 'checkout', requiredScenarioState: 'Done' }),
        stubMapping(3),
      ]),
    );
    mount({
      scenarios: { list: listing(scenario('checkout', 'Ready', ['Ready', 'Done'])) },
      mappings: { paginate: paginateSpy },
    });
    await screen.findByRole('heading', { name: 'checkout' });

    expect(paginateSpy).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Which stubs serve in each state' }));

    await waitFor(() => expect(paginateSpy).toHaveBeenCalledTimes(1));
    expect(await screen.findByText('serves in Ready, then moves to Done')).toBeInTheDocument();
    expect(screen.getByText('serves in Done')).toBeInTheDocument();
  });

  it('asks before returning every scenario to Started, and says whose work that is', async () => {
    const user = userEvent.setup();
    const reset = vi.fn().mockResolvedValue(undefined);
    mount({
      scenarios: { list: listing(scenario('checkout', 'Ready', ['Ready'])), reset },
    });
    await screen.findByRole('heading', { name: 'checkout' });

    await user.click(screen.getByRole('button', { name: 'Reset all scenarios' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/not scoped to you/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    expect(reset).not.toHaveBeenCalled();
  });

  it('explains a store outage as a refused write rather than a broken page', async () => {
    const user = userEvent.setup();
    const setState = vi
      .fn()
      .mockRejectedValue(adminError(503, [{ code: 1020, title: 'Store unavailable' }]));
    mount({
      scenarios: { list: listing(scenario('checkout', 'Ready', ['Ready', 'Done'])), setState },
    });
    await screen.findByRole('heading', { name: 'checkout' });

    await user.click(screen.getByRole('button', { name: 'Set checkout to Done' }));

    expect(
      await screen.findByRole('heading', { name: 'The stub store is unavailable' }),
    ).toBeInTheDocument();
    // The cards stay: reads keep serving from the snapshot through an outage,
    // so blanking the page would overstate what broke.
    expect(screen.getByRole('heading', { name: 'checkout' })).toBeInTheDocument();
  });

  it('says a deployment with no scenarios is not a deployment with a problem', async () => {
    mount({ scenarios: { list: listing() } });

    expect(await screen.findByRole('heading', { name: 'No scenarios' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });
});
