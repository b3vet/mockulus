// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { ErrorCode, ErrorCodeStatus, type ErrorCodeValue } from '../../src/codes.js';

/**
 * The codes are transcribed from SPEC Appendix B, and a transcription is a copy
 * that was right once. This reads the table out of the spec and holds the
 * exported constants to it in both directions, so a code added to the server
 * without being added here — or removed from the spec and left here — fails the
 * SDK's own test rather than being discovered by a consumer branching on a
 * number that no longer means anything.
 *
 * It is the same machine-checked-truth pattern the Go side uses for the
 * behavior catalog and the compatibility matrix, applied to the one table this
 * package copies out of the spec.
 */
const specPath = fileURLToPath(new URL('../../../../SPEC.md', import.meta.url));

/** Reads the `| code | status | meaning |` rows out of Appendix B. */
function appendixBCodes(): Map<number, number> {
  const spec = readFileSync(specPath, 'utf8');
  const start = spec.indexOf('## Appendix B');
  expect(start, 'Appendix B should be in SPEC.md').toBeGreaterThan(-1);
  const end = spec.indexOf('## Appendix C', start);
  const section = spec.slice(start, end === -1 ? undefined : end);

  const codes = new Map<number, number>();
  for (const line of section.split('\n')) {
    // A data row is `| 1001 | 404 | Unsupported admin endpoint … |`. The header
    // and the `|---|` separator both fail the numeric parse, so they need no
    // special case.
    const cells = line.split('|').map((c) => c.trim());
    if (cells.length < 4) continue;
    const code = Number(cells[1]);
    const status = Number(cells[2]);
    if (!Number.isInteger(code) || !Number.isInteger(status)) continue;
    codes.set(code, status);
  }
  return codes;
}

describe('the Appendix B error catalog', () => {
  const fromSpec = appendixBCodes();

  it('is parsed out of the spec at all', () => {
    // Without this the whole suite passes vacuously the day the table moves or
    // changes shape: an empty map satisfies every "for each" below.
    expect(fromSpec.size).toBeGreaterThanOrEqual(15);
    expect(fromSpec.get(1001)).toBe(404);
  });

  it('has an exported constant for every code the spec lists', () => {
    const exported = new Set<number>(Object.values(ErrorCode));
    const missing = [...fromSpec.keys()].filter((code) => !exported.has(code));
    expect(missing, 'codes in SPEC Appendix B with no ErrorCode constant').toEqual([]);
  });

  it('exports no code the spec does not list', () => {
    const invented = Object.entries(ErrorCode).filter(([, code]) => !fromSpec.has(code));
    expect(invented, 'ErrorCode constants absent from SPEC Appendix B').toEqual([]);
  });

  it('agrees with the spec about which HTTP status each code carries', () => {
    for (const [code, status] of fromSpec) {
      expect(ErrorCodeStatus[code as ErrorCodeValue], `HTTP status for code ${code}`).toBe(status);
    }
  });

  it('maps a status for every code it exports', () => {
    for (const code of Object.values(ErrorCode)) {
      expect(ErrorCodeStatus[code], `code ${code} has no status`).toBeTypeOf('number');
    }
  });
});
