// SPDX-License-Identifier: Apache-2.0

/**
 * Handing a file to the browser.
 *
 * There are two of these and there stay two, and the reason is worth writing
 * down because at a glance they look like the same function twice. They were
 * two modules — `download.ts` and `ops-download.ts` — while two stages were
 * building two panels, which is the artefact this file removes: the mechanism
 * below was written out twice, so the argument for a detached anchor and
 * against a `data:` URL was made twice and could be revised in one place only.
 * That is now stated once.
 *
 * The *functions* stay two because the difference between them is a difference
 * in the payload rather than in the code. {@link downloadTextFile} writes a
 * document this page assembled and therefore knows the media type of, and it
 * takes text. {@link downloadBytes} writes a response-body file out of the
 * store, which is opaque bytes — the server holds them without interpretation
 * and answers `application/octet-stream` whatever they are — so there is no
 * media type to declare, and the stored name has to be reduced to something a
 * filesystem can hold. Collapsing the two into one entry point would mean
 * either sending stored bytes through a text path, which round-trips anything
 * that is not valid UTF-8 into replacement characters and hands the operator a
 * file the deployment does not serve, or a single signature with two arguments
 * that are only ever meaningful one at a time.
 */

/**
 * The mechanism both callers share.
 *
 * A blob URL and a synthetic anchor click, rather than a link to an endpoint:
 * for the export there is no endpoint — it is assembled in the page from the
 * snapshot already read — and for a stored file the endpoint needs the admin
 * token, which a browser cannot attach to a navigation.
 *
 * `data:` URLs were the other option and are worse in two ways that matter:
 * they are size-limited by the browser, and the whole payload ends up in the
 * address bar and in history, where — for a deployment whose stubs carry
 * authentication headers in their `request` criteria — it should not be.
 */
function handOver(fileName: string, blob: Blob): void {
  const url = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = fileName;
    // Not appended to the document. A click on a detached anchor still starts
    // the download in every browser this UI targets, and appending would mean a
    // node briefly in the tree that Svelte does not own — the kind of thing that
    // survives a failure halfway through.
    anchor.click();
  } finally {
    // The blob is held alive by the URL until it is revoked, and the download
    // has already taken its own reference by the time `click` returns.
    URL.revokeObjectURL(url);
  }
}

/**
 * A document this page assembled, with the media type it knows it wrote.
 *
 * Used for the stub export, which is exactly the set the list is showing —
 * filters included — and which a server-side download could not be.
 */
export function downloadTextFile(fileName: string, mediaType: string, text: string): void {
  handOver(fileName, new Blob([text], { type: mediaType }));
}

/**
 * A stored response-body file, verbatim.
 *
 * The bytes are not decoded on the way through, because the store does not
 * interpret them either: a body is as likely to be a PNG or a protobuf as it is
 * to be JSON, and the file the operator saves has to be the file the deployment
 * serves.
 */
export function downloadBytes(fileName: string, bytes: ArrayBuffer): void {
  // A stored name may contain slashes — the store holds names rather than paths
  // — and a browser reads a slash in `download` as a directory separator it
  // will not create. The last segment is the file the operator expects to find
  // in their downloads folder.
  handOver(lastSegment(fileName), new Blob([bytes], { type: 'application/octet-stream' }));
}

/** The part of a stored name a filesystem can hold, with a fallback for a name that is all slashes. */
export function lastSegment(name: string): string {
  const parts = name.split('/').filter((part) => part !== '');
  return parts[parts.length - 1] ?? 'download';
}
