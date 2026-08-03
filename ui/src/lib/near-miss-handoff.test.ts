// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { offerDraft, takeDraft } from './near-miss-handoff';
import { emptyDraft } from './near-miss-model';

describe('the journal-to-debugger handoff', () => {
  it('has nothing to offer until the journal offers something', () => {
    expect(takeDraft()).toBeUndefined();
  });

  it('hands over what was offered', () => {
    const draft = { ...emptyDraft(), url: '/api/orders' };
    offerDraft(draft);

    expect(takeDraft()).toEqual(draft);
  });

  it('gives it up only once', () => {
    // Take-once is what keeps this from being state. A reader who opens the
    // debugger from the journal, navigates away and comes back should get an
    // empty form rather than a request they have finished with.
    offerDraft({ ...emptyDraft(), url: '/api/orders' });
    takeDraft();

    expect(takeDraft()).toBeUndefined();
  });

  it('keeps only the latest offer', () => {
    offerDraft({ ...emptyDraft(), url: '/first' });
    offerDraft({ ...emptyDraft(), url: '/second' });

    expect(takeDraft()?.url).toBe('/second');
  });
});
