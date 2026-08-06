// SPDX-License-Identifier: Apache-2.0
import type { StubMapping, StubMappingImport } from '@mockulus/admin-sdk';
import { describeJson } from './stub-draft';

/**
 * Moving mappings between a deployment and a file.
 *
 * The file format is the import endpoint's own body — `{"mappings": [...]}` —
 * and not a shape invented here. An export is therefore something the server can
 * be handed back without translation, by this UI, by `curl`, or by the SDK's
 * `mappings.import`, which is the only property that makes an export worth
 * having.
 *
 * Nothing here is reactive or touches the network.
 */

/** Two spaces, so an exported file is readable in a diff rather than one long line. */
const INDENT = 2;

/** What the exported document is, on the wire and on disk. */
export const EXPORT_MEDIA_TYPE = 'application/json';

/**
 * The file an export is offered under.
 *
 * Dated rather than unique, because the thing a reader wants from a directory of
 * these is to see which is the newer, and a random suffix answers a question
 * nobody asked. Two exports on one day overwrite each other in the browser's own
 * numbering scheme, which is the browser's business.
 */
export function exportFileName(now: Date): string {
  const date = [
    now.getFullYear(),
    String(now.getMonth() + 1).padStart(2, '0'),
    String(now.getDate()).padStart(2, '0'),
  ].join('-');
  return `mockulus-mappings-${date}.json`;
}

/**
 * The document to write out.
 *
 * `id` is kept. An export exists to be re-imported, and the import's duplicate
 * handling is defined over ids: stripping them would turn every restore into a
 * second copy of every stub, silently, on a deployment that already had them.
 */
export function toExportDocument(mappings: readonly StubMapping[]): string {
  return `${JSON.stringify({ mappings }, null, INDENT)}\n`;
}

/** A file that can be sent to the import endpoint, or the reason it cannot. */
export type ImportParse =
  | { readonly ok: true; readonly batch: StubMappingImport; readonly count: number }
  | { readonly ok: false; readonly message: string };

/**
 * Reads a file as an import batch.
 *
 * Deliberately strict about the envelope. A bare array is *not* quietly wrapped
 * in `{"mappings": …}`, even though doing so would be three lines and would make
 * one more file work: the server refuses that document, and a UI that reshapes
 * input the API would have rejected teaches its user a format that only works
 * here. Saying exactly what is wrong and exactly what to write is the same
 * fail-loud contract the 422s on this surface are built around (SPEC §2, P3).
 *
 * What is not checked is the mappings themselves. Their validity is the server's
 * to decide, and it decides it for every one of them at once — which is the
 * property the import failure surface is built to show.
 */
export function parseImportDocument(text: string): ImportParse {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    return {
      ok: false,
      message: `This file is not JSON: ${err instanceof Error ? err.message : String(err)}`,
    };
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return {
      ok: false,
      message:
        `An import document is a JSON object with a "mappings" array; this file is ` +
        `${describeJson(parsed)}. Wrap it as {"mappings": […]}.`,
    };
  }
  const mappings: unknown = (parsed as Record<string, unknown>).mappings;
  if (!Array.isArray(mappings)) {
    return {
      ok: false,
      message:
        mappings === undefined
          ? 'This file has no "mappings" key. An import document is {"mappings": […]}.'
          : `"mappings" must be an array; here it is ${describeJson(mappings)}.`,
    };
  }
  // `importOptions` is carried through untouched when the file names one, so a
  // document written for `curl` behaves identically here — including
  // `deleteAllNotInImport`, which is the one option that removes stubs.
  const options: unknown = (parsed as Record<string, unknown>).importOptions;
  const batch = {
    mappings: mappings as StubMapping[],
    ...(options === undefined ? {} : { importOptions: options }),
  } as StubMappingImport;
  return { ok: true, batch, count: mappings.length };
}

/** A problem pointer on an import, split into which mapping it names and where inside it. */
export interface ImportPointerParts {
  /** The index in the submitted `mappings` array, or `undefined` for a pointer about the batch. */
  readonly index: number | undefined;
  /** The pointer as it would read against that one mapping. `''` when it names the whole mapping. */
  readonly within: string;
}

/**
 * Splits an import pointer such as `/mappings/3/request/multipartPatterns`.
 *
 * The server prefixes every problem from a batch with the element's index
 * (`internal/admin/mappings_write.go`), which is the only thing that tells the
 * reader *which* of forty mappings in a file is at fault. Undoing the prefix
 * gives back the pointer as it would have read for a single-stub write, so the
 * same rendering serves both surfaces.
 */
export function importPointerParts(pointer: string): ImportPointerParts {
  const match = /^\/mappings\/(0|[1-9][0-9]*)(\/.*)?$/.exec(pointer);
  if (!match) {
    return { index: undefined, within: pointer };
  }
  return { index: Number(match[1]), within: match[2] ?? '' };
}
