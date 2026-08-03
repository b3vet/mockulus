// SPDX-License-Identifier: Apache-2.0
import type { StubMapping } from '@mockulus/admin-sdk';
import { describe, expect, it } from 'vitest';
import {
  describeMembership,
  isAtStart,
  isCurrent,
  membersOf,
  movedCount,
  servingIn,
  STARTED,
} from './scenario-panel';
import { scenario } from './ops-testing';
import { stubMapping } from './testing';

function member(index: number, overrides: Partial<StubMapping>): StubMapping {
  return stubMapping(index, { scenarioName: 'checkout', ...overrides });
}

describe('membersOf', () => {
  it('takes only the stubs naming the scenario, in the listing order', () => {
    const mappings = [
      member(1, {}),
      stubMapping(2),
      member(3, { scenarioName: 'other' }),
      member(4, {}),
    ];

    expect(membersOf(mappings, 'checkout').map((m) => m.id)).toEqual([
      mappings[0]?.id,
      mappings[3]?.id,
    ]);
  });

  it('answers nothing for a scenario no stub names', () => {
    expect(membersOf([stubMapping(1)], 'checkout')).toEqual([]);
  });
});

describe('servingIn', () => {
  const gated = member(1, { requiredScenarioState: 'Ready' });
  const ungated = member(2, {});
  const elsewhere = member(3, { requiredScenarioState: 'Done' });
  const members = [gated, ungated, elsewhere];

  it('includes a stub whose required state is the one asked about', () => {
    expect(servingIn(members, 'Ready').map((m) => m.id)).toEqual([gated.id, ungated.id]);
  });

  it('includes a stub with no required state in every state, since nothing gates it', () => {
    // SPEC §9.2: the scenario state is consulted only for a candidate that
    // declares a required one. A member with no gate is eligible throughout,
    // which is the case a reader is most likely to get wrong.
    for (const state of [STARTED, 'Ready', 'Done']) {
      expect(servingIn(members, state).map((m) => m.id)).toContain(ungated.id);
    }
  });

  it('leaves out a stub gated on some other state', () => {
    expect(servingIn(members, 'Ready').map((m) => m.id)).not.toContain(elsewhere.id);
  });
});

describe('isCurrent and isAtStart', () => {
  it('names the state the scenario is in', () => {
    const item = scenario('checkout', 'Ready', ['Ready', 'Done']);

    expect(isCurrent(item, 'Ready')).toBe(true);
    expect(isCurrent(item, 'Done')).toBe(false);
    expect(isAtStart(item)).toBe(false);
  });

  it('reads a scenario nothing has moved as being at the start', () => {
    expect(isAtStart(scenario('checkout', STARTED, ['Ready']))).toBe(true);
  });
});

describe('movedCount', () => {
  it('counts the scenarios a reset would actually change', () => {
    expect(
      movedCount([
        scenario('a', STARTED, ['Ready']),
        scenario('b', 'Ready', ['Ready']),
        scenario('c', 'Done', ['Done']),
      ]),
    ).toBe(2);
  });
});

describe('describeMembership', () => {
  it('says a stub with no gate serves everywhere', () => {
    expect(describeMembership(member(1, {}))).toBe('serves in any state');
  });

  it('names the gate and the transition when a stub has both', () => {
    expect(
      describeMembership(member(1, { requiredScenarioState: 'Ready', newScenarioState: 'Done' })),
    ).toBe('serves in Ready, then moves to Done');
  });

  it('describes a transition with no gate, which is a legal stub on its own', () => {
    expect(describeMembership(member(1, { newScenarioState: 'Done' }))).toBe(
      'serves in any state, then moves to Done',
    );
  });
});
