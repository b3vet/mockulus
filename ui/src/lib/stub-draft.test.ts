// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import {
  NEW_STUB_TEMPLATE,
  draftFor,
  duplicateNameOf,
  editorModeOf,
  parseDraft,
} from './stub-draft';
import { stubMapping } from './testing';

describe('editorModeOf', () => {
  it('reads the three editor paths', () => {
    expect(editorModeOf('/stubs/new')).toBe('create');
    expect(editorModeOf('/stubs/abc/edit')).toBe('edit');
    expect(editorModeOf('/stubs/abc/duplicate')).toBe('duplicate');
  });

  it('claims nothing outside them, so the detail and list views keep their paths', () => {
    expect(editorModeOf('/stubs')).toBeUndefined();
    expect(editorModeOf('/stubs/abc')).toBeUndefined();
    expect(editorModeOf('/stubs/abc/edit/extra')).toBeUndefined();
    expect(editorModeOf('/about')).toBeUndefined();
  });
});

describe('NEW_STUB_TEMPLATE', () => {
  it('is a stub that would register as it stands', () => {
    const parsed = parseDraft(NEW_STUB_TEMPLATE);
    expect(parsed.ok).toBe(true);
    if (parsed.ok) {
      expect(parsed.mapping.request?.method).toBe('GET');
      expect(parsed.mapping.response?.status).toBe(200);
    }
  });
});

describe('draftFor', () => {
  it('shows an edit the document exactly as the server holds it, identity included', () => {
    const mapping = stubMapping(1);
    const draft = JSON.parse(draftFor(mapping, 'edit')) as Record<string, unknown>;

    expect(draft).toEqual(mapping);
  });

  it('strips both spellings of the identity from a duplicate', () => {
    const mapping = stubMapping(1, { uuid: '00000000-0000-4000-8000-000000000001' });
    const draft = JSON.parse(draftFor(mapping, 'duplicate')) as Record<string, unknown>;

    // With either left in, the first save would be a create against an id that
    // already exists — refused with code 109 for a mistake this side could see.
    expect(draft).not.toHaveProperty('id');
    expect(draft).not.toHaveProperty('uuid');
    expect(draft.request).toEqual(mapping.request);
  });

  it('renames a duplicate, so the list does not show two identical rows', () => {
    const draft = JSON.parse(draftFor(stubMapping(1, { name: 'orders' }), 'duplicate')) as {
      name: string;
    };

    expect(draft.name).toBe('orders (copy)');
  });

  it('names an unnamed duplicate rather than leaving the field empty', () => {
    expect(duplicateNameOf(undefined)).toBe('Copy');
    expect(duplicateNameOf('  ')).toBe('Copy');
  });
});

describe('parseDraft', () => {
  it('accepts an object and hands back what was parsed', () => {
    const parsed = parseDraft('{"request": {"method": "POST"}}');
    expect(parsed).toEqual({ ok: true, mapping: { request: { method: 'POST' } } });
  });

  it('reports text that is not JSON, keeping the engine’s own message', () => {
    const parsed = parseDraft('{"request":');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) {
      expect(parsed.message).not.toBe('');
    }
  });

  it('refuses an array and says where a batch of mappings belongs instead', () => {
    const parsed = parseDraft('[{"request": {}}]');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) {
      expect(parsed.message).toContain('Import');
    }
  });

  it('refuses a scalar by naming what it found', () => {
    const parsed = parseDraft('42');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) {
      expect(parsed.message).toContain('a number');
    }
  });

  it('leaves unknown fields to the server, which is the authority on them', () => {
    // `equalToXml` is a real WireMock matcher mockulus does not implement. It
    // must reach the server, which answers 422 code 1000 with a pointer — a
    // check written here would be a second, staler copy of that rule.
    const parsed = parseDraft('{"request": {"bodyPatterns": [{"equalToXml": "<a/>"}]}}');
    expect(parsed.ok).toBe(true);
  });
});
