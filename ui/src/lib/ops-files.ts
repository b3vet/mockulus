// SPDX-License-Identifier: Apache-2.0
import type { StubMapping } from '@mockulus/admin-sdk';

/**
 * The files manager's arithmetic, with no Svelte and no network in it: which
 * stubs point at which file, and how many bytes that is.
 */

/**
 * The stubs whose `response.bodyFileName` names a given file.
 *
 * This is the back-link the files manager shows, and it is worth computing
 * because the consequence of getting it wrong is invisible until traffic
 * arrives: existence is deliberately not checked at registration, so a stub may
 * name a file that was never uploaded, and deleting a file leaves the stubs
 * referencing it serving 500 with code 1022 rather than failing to load. A
 * reader about to delete a file needs to see both directions of that — which
 * stubs will break, and which names are referenced by nothing at all.
 *
 * A `Map` rather than a plain object because a file name is arbitrary text from
 * whoever drove the mock: `__proto__` and `constructor` are legal names for a
 * response body, and on an object literal one of them is not a key.
 */
export function referencesByFile(mappings: readonly StubMapping[]): Map<string, StubMapping[]> {
  const index = new Map<string, StubMapping[]>();
  for (const mapping of mappings) {
    const name = mapping.response?.bodyFileName;
    if (name === undefined || name === '') {
      continue;
    }
    const existing = index.get(name);
    if (existing) {
      existing.push(mapping);
    } else {
      index.set(name, [mapping]);
    }
  }
  return index;
}

/**
 * The names stubs reference that the store does not hold.
 *
 * These are the stubs already serving code 1022, and the manager lists them
 * beside the stored files because the fix — upload a file under this name — is
 * the one action on this panel, and nothing else in the UI says the reference
 * is dangling.
 */
export function danglingReferences(
  mappings: readonly StubMapping[],
  stored: readonly string[],
): string[] {
  const have = new Set(stored);
  const missing = new Set<string>();
  for (const name of referencesByFile(mappings).keys()) {
    if (!have.has(name)) {
      missing.add(name);
    }
  }
  return [...missing].sort((a, b) => a.localeCompare(b));
}

/**
 * A byte count as a person reads one.
 *
 * Powers of two with the IEC names, because this is a stored payload rather
 * than a disk vendor's capacity, and because the server's own cap on an admin
 * body is written the same way.
 */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} ${bytes === 1 ? 'byte' : 'bytes'}`;
  }
  const units = ['KiB', 'MiB', 'GiB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  // One decimal place: enough to tell 1.2 MiB from 1.9 MiB, few enough that the
  // number does not imply a precision the rounding does not have.
  return `${value.toFixed(1)} ${units[unit]}`;
}
