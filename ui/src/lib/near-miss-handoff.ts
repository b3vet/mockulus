// SPDX-License-Identifier: Apache-2.0
import type { ComposeDraft } from './near-miss-model';

/**
 * How the journal hands an unmatched entry to the near-miss debugger.
 *
 * The two surfaces are separate routes, so the request has to travel somehow,
 * and the obvious carrier — a query parameter — is not available: this app's
 * router is path-only, and a whole request with its headers and body does not
 * belong in a URL that ends up in history and in every access log between the
 * browser and the server anyway.
 *
 * So it is a module-level slot, deliberately small and deliberately
 * take-once. Both views are in one bundle and one page load, which is the whole
 * lifetime this value needs. Take-once is what keeps it from being state: a
 * reader who opens the debugger from the journal, then navigates away and comes
 * back, should get an empty form rather than a request they have finished with.
 */
let pending: ComposeDraft | undefined;

/** Offers a draft to whichever debugger opens next. Replaces any earlier one. */
export function offerDraft(draft: ComposeDraft): void {
  pending = draft;
}

/** Takes the offered draft, if there is one, and clears the slot. */
export function takeDraft(): ComposeDraft | undefined {
  const draft = pending;
  pending = undefined;
  return draft;
}
