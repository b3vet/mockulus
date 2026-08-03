// SPDX-License-Identifier: Apache-2.0

/**
 * The three deployment-wide destructive calls, described well enough that
 * nobody presses one by mistake, and the rule that decides whether a typed
 * confirmation counts.
 *
 * This is a table rather than three buttons with prose beside them because the
 * thing that makes these dangerous is shared: mockulus is built for a
 * deployment several suites point at, and SPEC §1 tells users to namespace by
 * URL prefix precisely so they never have to reset. Every one of these calls
 * destroys work belonging to people who are not in the room. So each entry has
 * to say the same three things — which call it is, what goes, and what survives
 * — and a table is what makes it impossible to write one of them and forget.
 *
 * The phrase is per-action and deliberately different in each, so that having
 * typed one of them does not teach the fingers the next. Retyping a phrase is
 * not security; it is the pause in which somebody reads the sentence above it.
 */

export type DangerActionId = 'journal-clear' | 'mappings-reset' | 'reset-all';

export interface DangerAction {
  readonly id: DangerActionId;
  /** What the button on the page says. */
  readonly label: string;
  /** The heading of the confirmation, phrased as the question it is. */
  readonly title: string;
  /** The admin call, named so the reader can tell the three apart by endpoint and not only by label. */
  readonly endpoint: string;
  /** The sentence that must be typed before the action can be run. */
  readonly phrase: string;
  /** What this destroys, deployment-wide. */
  readonly destroys: readonly string[];
  /** What it leaves alone — as load-bearing as the list above, because it bounds the damage. */
  readonly keeps: readonly string[];
}

/**
 * In increasing blast radius, which is also the order somebody scanning the
 * panel should meet them: the reader walks up to the one that takes everything
 * rather than finding it first.
 */
export const DANGER_ACTIONS: readonly DangerAction[] = [
  {
    id: 'journal-clear',
    label: 'Clear the journal',
    title: 'Clear the request journal?',
    endpoint: 'DELETE /__admin/requests',
    phrase: 'clear the journal',
    destroys: [
      'Every serve event recorded so far, for every caller of this deployment — including entries another suite is about to verify against.',
    ],
    keeps: [
      'Every stub, persistent or not.',
      'Every scenario state.',
      'The response-body files and the global settings.',
    ],
  },
  {
    id: 'mappings-reset',
    label: 'Reset the mappings',
    title: 'Sweep every non-persistent stub?',
    endpoint: 'POST /__admin/mappings/reset',
    phrase: 'sweep the stubs',
    destroys: [
      'Every stub that did not ask to be persistent — which is the default, so in most deployments that is every stub anyone has registered.',
      'Other teams’ stubs along with yours. The call takes no filter.',
    ],
    keeps: [
      'Stubs registered with "persistent": true.',
      'The request journal.',
      'Every scenario state, the response-body files and the global settings.',
    ],
  },
  {
    id: 'reset-all',
    label: 'Reset the whole deployment',
    title: 'Reset the whole deployment?',
    endpoint: 'POST /__admin/reset',
    phrase: 'reset the whole deployment',
    destroys: [
      'Every non-persistent stub, exactly as the mappings reset does.',
      'The entire request journal.',
      'Every scenario state — every scenario goes back to Started mid-flow.',
    ],
    keeps: [
      'Stubs registered with "persistent": true.',
      'The response-body files and the global settings.',
    ],
  },
];

/**
 * Whether what was typed counts as the phrase.
 *
 * Compared with the outer whitespace trimmed, runs of inner whitespace
 * collapsed, and case ignored. The safety here is that somebody had to read a
 * specific sentence and reproduce it; a capital letter a phone keyboard added,
 * or a second space between two words, is not the mistake this guard is for,
 * and refusing those would train people to stop reading and start pasting.
 */
export function phraseMatches(typed: string, phrase: string): boolean {
  return normalize(typed) === normalize(phrase) && phrase !== '';
}

function normalize(text: string): string {
  return text.trim().replace(/\s+/g, ' ').toLowerCase();
}
