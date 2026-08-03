// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { DANGER_ACTIONS, phraseMatches } from './ops-danger';
import { formatTimestamp, formatUptime } from './ops-overview';

describe('DANGER_ACTIONS', () => {
  it('gives every action its own phrase, so typing one does not train the next', () => {
    const phrases = DANGER_ACTIONS.map((action) => action.phrase);

    expect(new Set(phrases).size).toBe(phrases.length);
  });

  it('says both what goes and what survives, for each of the three', () => {
    // A warning that only lists damage gives the reader no way to tell the
    // three apart, and "the journal, and not your stubs" is the difference
    // between a call somebody can take back and one they cannot.
    for (const action of DANGER_ACTIONS) {
      expect(action.destroys.length).toBeGreaterThan(0);
      expect(action.keeps.length).toBeGreaterThan(0);
      expect(action.endpoint).toMatch(/^(POST|DELETE) \/__admin\//);
    }
  });

  it('is ordered by increasing blast radius, ending at the one that takes everything', () => {
    expect(DANGER_ACTIONS.map((action) => action.id)).toEqual([
      'journal-clear',
      'mappings-reset',
      'reset-all',
    ]);
  });
});

describe('phraseMatches', () => {
  it('accepts the phrase as written', () => {
    expect(phraseMatches('reset the whole deployment', 'reset the whole deployment')).toBe(true);
  });

  it('forgives what a keyboard adds and the reader did not choose', () => {
    expect(phraseMatches('  Reset The Whole Deployment ', 'reset the whole deployment')).toBe(true);
    expect(phraseMatches('reset the  whole deployment', 'reset the whole deployment')).toBe(true);
  });

  it('refuses anything that is not the sentence', () => {
    expect(phraseMatches('', 'reset the whole deployment')).toBe(false);
    expect(phraseMatches('reset', 'reset the whole deployment')).toBe(false);
    expect(phraseMatches('reset the whole deployments', 'reset the whole deployment')).toBe(false);
    // One action's phrase must never unlock another's, which is the whole
    // reason they differ.
    expect(phraseMatches('clear the journal', 'reset the whole deployment')).toBe(false);
  });
});

describe('formatUptime', () => {
  it('keeps seconds only in the window where a restart is the question', () => {
    expect(formatUptime(0)).toBe('0s');
    expect(formatUptime(59.7)).toBe('59s');
  });

  it('reads longer uptimes in the units a person would say them in', () => {
    expect(formatUptime(60)).toBe('1m');
    expect(formatUptime(3600)).toBe('1h');
    expect(formatUptime(3661)).toBe('1h 1m');
    expect(formatUptime(90061)).toBe('1d 1h 1m');
  });

  it('does not invent a duration from a value it cannot read', () => {
    expect(formatUptime(-1)).toBe('unknown');
    expect(formatUptime(Number.NaN)).toBe('unknown');
  });
});

describe('formatTimestamp', () => {
  it('returns an unparseable value as it came rather than as Invalid Date', () => {
    expect(formatTimestamp('not a time')).toBe('not a time');
  });

  it('renders a real timestamp as something other than the raw wire form', () => {
    expect(formatTimestamp('2026-07-29T10:00:00Z')).not.toBe('2026-07-29T10:00:00Z');
  });
});
