// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { draftFrom, emptyDraft, isUnset, summarize, toSettings } from './ops-settings';

describe('draftFrom', () => {
  it('reads a document that configures nothing as an empty form', () => {
    expect(draftFrom({})).toEqual(emptyDraft());
    expect(draftFrom(undefined)).toEqual(emptyDraft());
  });

  it('selects the shape the stored distribution has, and fills only that shape', () => {
    const uniform = draftFrom({
      fixedDelay: 20,
      delayDistribution: { type: 'uniform', lower: 5, upper: 9 },
    });
    expect(uniform).toMatchObject({ fixedDelay: '20', kind: 'uniform', lower: '5', upper: '9' });
    expect(uniform.median).toBe('');

    const lognormal = draftFrom({
      delayDistribution: { type: 'lognormal', median: 90, sigma: 0.4 },
    });
    expect(lognormal).toMatchObject({ kind: 'lognormal', median: '90', sigma: '0.4' });
    expect(lognormal.lower).toBe('');
  });
});

describe('toSettings', () => {
  it('round-trips a document through the form unchanged', () => {
    // The property that matters, because the endpoint replaces rather than
    // merges: opening the form and pressing Save must not change what is
    // stored. A field the form dropped would be a field the save cleared.
    for (const settings of [
      {},
      { fixedDelay: 0 },
      { fixedDelay: 250 },
      { delayDistribution: { type: 'uniform' as const, lower: 10, upper: 40 } },
      { fixedDelay: 5, delayDistribution: { type: 'lognormal' as const, median: 90, sigma: 0.1 } },
    ]) {
      const result = toSettings(draftFrom(settings));
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.settings).toEqual(settings);
      }
    }
  });

  it('omits an empty fixed delay rather than sending a zero', () => {
    // Zero is a delay somebody asked for; empty is the absence of one. Sending
    // 0 for an untouched box would write a document the operator did not.
    const result = toSettings({ ...emptyDraft(), fixedDelay: '' });

    expect(result).toEqual({ ok: true, settings: {} });
  });

  it('refuses text that is not a number, naming the field it came from', () => {
    expect(toSettings({ ...emptyDraft(), fixedDelay: 'soon' })).toMatchObject({
      ok: false,
      field: 'fixedDelay',
    });
    // `parseInt` would read this as 12 and send a delay nobody wrote.
    expect(toSettings({ ...emptyDraft(), fixedDelay: '12ms' })).toMatchObject({
      ok: false,
      field: 'fixedDelay',
    });
    expect(toSettings({ ...emptyDraft(), fixedDelay: '-1' })).toMatchObject({
      ok: false,
      field: 'fixedDelay',
    });
    expect(toSettings({ ...emptyDraft(), fixedDelay: '1.5' })).toMatchObject({
      ok: false,
      field: 'fixedDelay',
    });
  });

  it('requires both bounds of a uniform distribution, since the contract does', () => {
    expect(toSettings({ ...emptyDraft(), kind: 'uniform', lower: '5', upper: '' })).toMatchObject({
      ok: false,
      field: 'upper',
    });
  });

  it('accepts a fractional sigma, which is the one value that is legitimately not whole', () => {
    const result = toSettings({ ...emptyDraft(), kind: 'lognormal', median: '90', sigma: '0.35' });

    expect(result).toEqual({
      ok: true,
      settings: { delayDistribution: { type: 'lognormal', median: 90, sigma: 0.35 } },
    });
  });

  it('leaves an upper bound below its lower one to the server', () => {
    // Deliberately not refused here. The server owns that rule and answers it
    // with a pointer at the field; a copy of it in the browser is a second
    // authority that can drift from the first.
    const result = toSettings({ ...emptyDraft(), kind: 'uniform', lower: '90', upper: '10' });

    expect(result).toEqual({
      ok: true,
      settings: { delayDistribution: { type: 'uniform', lower: 90, upper: 10 } },
    });
  });

  it('drops the fields of a shape that is not selected', () => {
    const result = toSettings({
      ...emptyDraft(),
      kind: 'none',
      lower: '5',
      upper: '9',
      median: '90',
    });

    expect(result).toEqual({ ok: true, settings: {} });
  });
});

describe('isUnset and summarize', () => {
  it('reads an empty document as configuring nothing', () => {
    expect(isUnset({})).toBe(true);
    expect(isUnset(undefined)).toBe(true);
    expect(isUnset({ fixedDelay: 0 })).toBe(false);
  });

  it('describes what a matched response will actually wait out', () => {
    expect(summarize({})).toContain('No deployment-wide delay');
    expect(summarize({ fixedDelay: 250 })).toContain('a fixed 250 ms');
    expect(summarize({ delayDistribution: { type: 'uniform', lower: 5, upper: 9 } })).toContain(
      'a uniform 5–9 ms sample',
    );
    expect(
      summarize({
        fixedDelay: 5,
        delayDistribution: { type: 'lognormal', median: 90, sigma: 0.1 },
      }),
    ).toContain('a fixed 5 ms plus a log-normal sample');
  });
});
