// SPDX-License-Identifier: Apache-2.0

/**
 * Handing a file to the browser.
 *
 * A blob URL and a synthetic anchor click, rather than a link to an endpoint,
 * because there is no endpoint: the export is assembled in the page from the
 * snapshot already read, and the admin API has no route that answers a file.
 * Building it here also means the export is exactly the set the list is showing,
 * filters included, which a server-side download could not be.
 *
 * `data:` URLs were the other option and are worse in two ways that matter: they
 * are size-limited by the browser, and the whole document ends up in the address
 * bar and in history — where, for a deployment whose stubs carry authentication
 * headers in their `request` criteria, it should not be.
 */
export function downloadTextFile(fileName: string, mediaType: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: mediaType }));
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
