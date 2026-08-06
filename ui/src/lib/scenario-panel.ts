// SPDX-License-Identifier: Apache-2.0
import type { Scenario, StubMapping } from '@mockulus/admin-sdk';

/**
 * Reading a set of scenarios and the stubs behind them, with no Svelte and no
 * network in it. The panel is mostly this, and this is the half that can be
 * tested against the awkward documents — a scenario whose stubs gate on nothing,
 * a stub that names a scenario and no state at all — without mounting anything.
 */

/**
 * The state every scenario begins in and returns to.
 *
 * It is spelled out here rather than read off a scenario because the server
 * guarantees it is in every `possibleStates`: the snapshot adds it whether or
 * not a stub names it, so a scenario can always be driven back (SPEC §9.1, and
 * deviation #34 — WireMock refuses the same write when no stub names the
 * state). The panel's per-scenario reset relies on that being true, and this
 * constant is what says so.
 */
export const STARTED = 'Started';

/**
 * The stubs that belong to a scenario, in the order the listing gave them.
 *
 * This has to be computed from a separate mappings read rather than taken from
 * the scenario document, because `GET /__admin/scenarios` deliberately does not
 * embed member stubs the way WireMock does (deviation #32): a scenario holding
 * a hundred stubs would repeat all hundred inside a listing whose caller wants
 * a state name.
 */
export function membersOf(mappings: readonly StubMapping[], name: string): StubMapping[] {
  return mappings.filter((mapping) => mapping.scenarioName === name);
}

/**
 * Which of a scenario's stubs are eligible to serve while it sits in `state`.
 *
 * A member with no `requiredScenarioState` is gated by nothing and so is
 * eligible in every state (SPEC §9.2 — the state is consulted only for a
 * candidate that declares a required one). Eligible is not the same as chosen:
 * priority and the rest of the request criteria still decide between two stubs
 * that are both eligible, so the panel says "serve here" and never "will
 * serve".
 */
export function servingIn(members: readonly StubMapping[], state: string): StubMapping[] {
  return members.filter(
    (mapping) =>
      mapping.requiredScenarioState === undefined || mapping.requiredScenarioState === state,
  );
}

/** Whether a state is the one the scenario is in, which is the one not worth a button. */
export function isCurrent(scenario: Scenario, state: string): boolean {
  return scenario.state === state;
}

/**
 * Whether the scenario is already where a reset would put it.
 *
 * Read from the state rather than from a comparison the caller writes, so the
 * panel and the reset button cannot disagree about what "already reset" means.
 */
export function isAtStart(scenario: Scenario): boolean {
  return scenario.state === STARTED;
}

/** How many scenarios have been moved off their initial state, for the page's summary line. */
export function movedCount(scenarios: readonly Scenario[]): number {
  return scenarios.filter((scenario) => !isAtStart(scenario)).length;
}

/**
 * How a member stub's gate reads on a card.
 *
 * The two halves are independent — a stub may gate without transitioning, or
 * transition without gating — so both are described rather than assuming the
 * pair, and the "any state" wording is what tells a reader that a stub with no
 * gate is not a stub that never serves.
 */
export function describeMembership(mapping: StubMapping): string {
  const serves =
    mapping.requiredScenarioState === undefined
      ? 'serves in any state'
      : `serves in ${mapping.requiredScenarioState}`;
  return mapping.newScenarioState === undefined
    ? serves
    : `${serves}, then moves to ${mapping.newScenarioState}`;
}
