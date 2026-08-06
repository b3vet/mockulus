// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import {
  exportFileName,
  importPointerParts,
  parseImportDocument,
  toExportDocument,
} from './mappings-file';
import { stubMapping, stubMappings } from './testing';

describe('toExportDocument', () => {
  it('writes the envelope the import endpoint takes, so a round trip needs no translation', () => {
    const text = toExportDocument(stubMappings(2));
    const parsed = parseImportDocument(text);

    expect(parsed.ok).toBe(true);
    if (parsed.ok) {
      expect(parsed.count).toBe(2);
    }
  });

  it('keeps ids, which is what makes a re-import a restore rather than a second copy', () => {
    const text = toExportDocument([stubMapping(1)]);
    expect(JSON.parse(text)).toEqual({ mappings: [stubMapping(1)] });
  });
});

describe('exportFileName', () => {
  it('is dated, so a directory of them sorts into the order they were taken', () => {
    expect(exportFileName(new Date(2026, 6, 4))).toBe('mockulus-mappings-2026-07-04.json');
  });
});

describe('parseImportDocument', () => {
  it('accepts the envelope and counts what is in it', () => {
    const parsed = parseImportDocument('{"mappings": [{"request": {}}, {"request": {}}]}');
    expect(parsed.ok).toBe(true);
    if (parsed.ok) {
      expect(parsed.count).toBe(2);
    }
  });

  it('carries importOptions through untouched', () => {
    const parsed = parseImportDocument(
      '{"mappings": [], "importOptions": {"duplicatePolicy": "IGNORE"}}',
    );
    expect(parsed.ok).toBe(true);
    if (parsed.ok) {
      expect(parsed.batch.importOptions).toEqual({ duplicatePolicy: 'IGNORE' });
    }
  });

  it('refuses a bare array rather than wrapping it, and says what to write', () => {
    // Wrapping would be three lines and would teach a format the server refuses.
    const parsed = parseImportDocument('[{"request": {}}]');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) {
      expect(parsed.message).toContain('{"mappings": […]}');
    }
  });

  it('names a missing mappings key rather than sending an empty batch', () => {
    const parsed = parseImportDocument('{"stubs": []}');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) {
      expect(parsed.message).toContain('no "mappings" key');
    }
  });

  it('reports a file that is not JSON as a file problem, not as a server one', () => {
    const parsed = parseImportDocument('not json');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) {
      expect(parsed.message).toContain('not JSON');
    }
  });

  it('leaves the mappings themselves to the server', () => {
    const parsed = parseImportDocument('{"mappings": [{"request": {"equalToXml": "<a/>"}}]}');
    expect(parsed.ok).toBe(true);
  });
});

describe('importPointerParts', () => {
  it('splits the index the server prefixed from the pointer inside that mapping', () => {
    expect(importPointerParts('/mappings/3/request/multipartPatterns')).toEqual({
      index: 3,
      within: '/request/multipartPatterns',
    });
  });

  it('reads a pointer at a whole mapping', () => {
    expect(importPointerParts('/mappings/0')).toEqual({ index: 0, within: '' });
  });

  it('leaves a pointer about the batch alone', () => {
    expect(importPointerParts('/mappings')).toEqual({ index: undefined, within: '/mappings' });
    expect(importPointerParts('/importOptions/duplicatePolicy')).toEqual({
      index: undefined,
      within: '/importOptions/duplicatePolicy',
    });
  });
});
