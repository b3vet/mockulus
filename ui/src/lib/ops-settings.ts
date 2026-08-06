// SPDX-License-Identifier: Apache-2.0
import type { Settings } from '@mockulus/admin-sdk';

/**
 * The settings form's document arithmetic, with no Svelte and no network in it:
 * turning the stored document into the fields of a form, and the fields of a
 * form back into a document the server will take.
 *
 * The whole of the supported settings surface is the deployment-wide response
 * delay — WireMock's other settings are refused by name with code 1005 rather
 * than accepted and ignored — so this is a small type with a large consequence,
 * because `POST /__admin/settings` **replaces** rather than merges. A form that
 * submitted only the field somebody edited would clear the other one, and the
 * only way to be sure it does not is to build the whole document every time.
 */

/**
 * The distribution shapes the contract admits, plus the absence of one.
 *
 * `none` is a state of the form rather than a value the server has: a document
 * with no `delayDistribution` is how the deployment says there is no sampled
 * delay, so the radio group needs a third option and the document does not.
 */
export type DistributionKind = 'none' | 'uniform' | 'lognormal';

/** The document as a form holds it: every number is the text somebody typed. */
export interface SettingsDraft {
  fixedDelay: string;
  kind: DistributionKind;
  lower: string;
  upper: string;
  median: string;
  sigma: string;
}

/**
 * A rejected draft names the field, so the form can put the message beside the
 * input that caused it rather than at the top where the reader has to guess.
 */
export type DraftField = 'fixedDelay' | 'lower' | 'upper' | 'median' | 'sigma';

export type DraftResult =
  | { readonly ok: true; readonly settings: Settings }
  | { readonly ok: false; readonly field: DraftField; readonly message: string };

/** What the form holds before anything is read, and after a Clear. */
export function emptyDraft(): SettingsDraft {
  return { fixedDelay: '', kind: 'none', lower: '', upper: '', median: '', sigma: '' };
}

/**
 * The stored document, as fields.
 *
 * The inputs of the shape that is *not* selected keep whatever they held rather
 * than being blanked, which is what lets somebody who switches from uniform to
 * log-normal and back find their bounds still there. That only matters on the
 * draft the user is editing; a draft built from the server has nothing to
 * preserve, so the unselected inputs start empty.
 */
export function draftFrom(settings: Settings | undefined): SettingsDraft {
  const draft = emptyDraft();
  if (!settings) {
    return draft;
  }
  if (settings.fixedDelay !== undefined) {
    draft.fixedDelay = String(settings.fixedDelay);
  }
  const distribution = settings.delayDistribution;
  if (distribution?.type === 'uniform') {
    draft.kind = 'uniform';
    draft.lower = String(distribution.lower);
    draft.upper = String(distribution.upper);
  } else if (distribution?.type === 'lognormal') {
    draft.kind = 'lognormal';
    draft.median = String(distribution.median);
    draft.sigma = String(distribution.sigma);
  }
  return draft;
}

/**
 * The form's fields, as a document to send — or the first field that is not a
 * number at all.
 *
 * What is checked here and what is left to the server is a deliberate split.
 * "Is this text a number" is a question about the form, and the server cannot
 * answer it any better than this can: an empty box or the word `soon` never
 * reaches a request at all. Everything else — an upper bound below its lower
 * one, a delay the deployment will not accept — is the server's judgement to
 * make, and it makes it with a JSON pointer naming the field. Copying those
 * rules here would create a second authority that can drift from the first,
 * which is precisely the accept-and-diverge failure the project refuses
 * elsewhere.
 */
export function toSettings(draft: SettingsDraft): DraftResult {
  const settings: Settings = {};

  const fixed = parseWholeMilliseconds(draft.fixedDelay);
  if (fixed.bad) {
    return refuse('fixedDelay', 'The fixed delay must be a whole number of milliseconds.');
  }
  if (fixed.value !== undefined) {
    settings.fixedDelay = fixed.value;
  }

  if (draft.kind === 'uniform') {
    const lower = parseWholeMilliseconds(draft.lower);
    const upper = parseWholeMilliseconds(draft.upper);
    if (lower.bad || lower.value === undefined) {
      return refuse('lower', 'The lower bound must be a whole number of milliseconds.');
    }
    if (upper.bad || upper.value === undefined) {
      return refuse('upper', 'The upper bound must be a whole number of milliseconds.');
    }
    settings.delayDistribution = { type: 'uniform', lower: lower.value, upper: upper.value };
  } else if (draft.kind === 'lognormal') {
    const median = parseWholeMilliseconds(draft.median);
    if (median.bad || median.value === undefined) {
      return refuse('median', 'The median must be a whole number of milliseconds.');
    }
    const sigma = parseDecimal(draft.sigma);
    if (sigma.bad || sigma.value === undefined) {
      return refuse('sigma', 'Sigma must be a number.');
    }
    settings.delayDistribution = { type: 'lognormal', median: median.value, sigma: sigma.value };
  }

  return { ok: true, settings };
}

/** Whether the stored document configures nothing, which is what zero-config answers. */
export function isUnset(settings: Settings | undefined): boolean {
  return (
    settings === undefined ||
    (settings.fixedDelay === undefined && settings.delayDistribution === undefined)
  );
}

/** The stored document in one line, for the panel's "what is in force" summary. */
export function summarize(settings: Settings | undefined): string {
  if (isUnset(settings)) {
    return 'No deployment-wide delay. Every matched response is served as fast as the replica can.';
  }
  const parts: string[] = [];
  if (settings?.fixedDelay !== undefined) {
    parts.push(`a fixed ${settings.fixedDelay} ms`);
  }
  const distribution = settings?.delayDistribution;
  if (distribution?.type === 'uniform') {
    parts.push(`a uniform ${distribution.lower}–${distribution.upper} ms sample`);
  } else if (distribution?.type === 'lognormal') {
    parts.push(
      `a log-normal sample, median ${distribution.median} ms, sigma ${distribution.sigma}`,
    );
  }
  return `Every matched response whose stub declares no delay of its own waits ${parts.join(' plus ')}.`;
}

function refuse(field: DraftField, message: string): DraftResult {
  return { ok: false, field, message };
}

/** A parsed field: absent when the box is empty, `bad` when it holds something that is not a number. */
interface Parsed {
  readonly value: number | undefined;
  readonly bad: boolean;
}

/**
 * A whole, non-negative count of milliseconds.
 *
 * `Number()` rather than `parseInt`, because `parseInt('12ms')` is 12 and a
 * delay somebody wrote units on would silently become a delay they did not
 * write. The negative and fractional cases are refused here as well, even
 * though the server refuses them too, because they are what a number input's
 * spinner produces by accident and the round trip buys nothing.
 */
function parseWholeMilliseconds(text: string): Parsed {
  const trimmed = text.trim();
  if (trimmed === '') {
    return { value: undefined, bad: false };
  }
  const value = Number(trimmed);
  if (!Number.isInteger(value) || value < 0) {
    return { value: undefined, bad: true };
  }
  return { value, bad: false };
}

/** Sigma, which is the one value in the document that is legitimately fractional. */
function parseDecimal(text: string): Parsed {
  const trimmed = text.trim();
  if (trimmed === '') {
    return { value: undefined, bad: false };
  }
  const value = Number(trimmed);
  if (!Number.isFinite(value) || value < 0) {
    return { value: undefined, bad: true };
  }
  return { value, bad: false };
}
