// SPDX-License-Identifier: Apache-2.0

/**
 * Handing a stored response-body file to the browser.
 *
 * The sibling of `download.ts`, and separate from it for a reason that is about
 * the payload rather than about the code: that one writes a document this page
 * assembled and therefore knows the media type of, and it takes text. A file in
 * the response-body store is opaque bytes — the server stores them without
 * interpretation and answers `application/octet-stream` whatever they are — so
 * there is no media type to declare and no encoding to decode through. Sending
 * these bytes through a text download would round-trip anything that is not
 * valid UTF-8 into replacement characters, and the file the operator saved
 * would not be the file the deployment serves.
 *
 * The mechanism is the same and is the same on purpose: a blob URL and a
 * synthetic anchor, never a `data:` URL, which is size-limited and puts the
 * whole payload in the address bar and in history.
 */
export function downloadBytes(fileName: string, bytes: ArrayBuffer): void {
  const url = URL.createObjectURL(new Blob([bytes], { type: 'application/octet-stream' }));
  try {
    const anchor = document.createElement('a');
    anchor.href = url;
    // A stored name may contain slashes — the store holds names rather than
    // paths — and a browser reads a slash in `download` as a directory
    // separator it will not create. The last segment is the file the operator
    // expects to find in their downloads folder.
    anchor.download = lastSegment(fileName);
    // Not appended to the document, on the same reasoning `download.ts`
    // records: a detached anchor still starts the download, and appending would
    // put a node Svelte does not own into the tree.
    anchor.click();
  } finally {
    // The blob is held alive by the URL until it is revoked, and the download
    // has already taken its own reference by the time `click` returns.
    URL.revokeObjectURL(url);
  }
}

/** The part of a stored name a filesystem can hold, with a fallback for a name that is all slashes. */
export function lastSegment(name: string): string {
  const parts = name.split('/').filter((part) => part !== '');
  return parts[parts.length - 1] ?? 'download';
}
