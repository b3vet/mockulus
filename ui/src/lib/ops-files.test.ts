// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it } from 'vitest';
import { danglingReferences, formatBytes, referencesByFile } from './ops-files';
import { lastSegment } from './ops-download';
import { stubMapping } from './testing';

function referencing(index: number, bodyFileName: string) {
  return stubMapping(index, { response: { status: 200, bodyFileName } });
}

describe('referencesByFile', () => {
  it('groups every stub that names a file under that name', () => {
    const index = referencesByFile([
      referencing(1, 'order.json'),
      stubMapping(2),
      referencing(3, 'order.json'),
      referencing(4, 'fixtures/large.bin'),
    ]);

    expect(index.get('order.json')).toHaveLength(2);
    expect(index.get('fixtures/large.bin')).toHaveLength(1);
    expect(index.size).toBe(2);
  });

  it('holds a name that would not be a key on an object literal', () => {
    // File names are arbitrary text from whoever drove the mock, and an object
    // would answer `Object.prototype.__proto__` here rather than the stubs.
    const index = referencesByFile([referencing(1, '__proto__'), referencing(2, 'constructor')]);

    expect(index.get('__proto__')).toHaveLength(1);
    expect(index.get('constructor')).toHaveLength(1);
  });

  it('ignores a stub with no body file, and an empty name', () => {
    const index = referencesByFile([stubMapping(1), referencing(2, '')]);

    expect(index.size).toBe(0);
  });
});

describe('danglingReferences', () => {
  it('names the files stubs point at that the store does not hold', () => {
    const mappings = [referencing(1, 'gone.json'), referencing(2, 'here.json')];

    expect(danglingReferences(mappings, ['here.json'])).toEqual(['gone.json']);
  });

  it('reports a missing name once however many stubs point at it', () => {
    const mappings = [referencing(1, 'gone.json'), referencing(2, 'gone.json')];

    expect(danglingReferences(mappings, [])).toEqual(['gone.json']);
  });

  it('answers nothing when every reference resolves', () => {
    expect(danglingReferences([referencing(1, 'here.json')], ['here.json', 'spare.json'])).toEqual(
      [],
    );
  });
});

describe('formatBytes', () => {
  it('counts small payloads exactly, because that is the size a fixture usually is', () => {
    expect(formatBytes(0)).toBe('0 bytes');
    expect(formatBytes(1)).toBe('1 byte');
    expect(formatBytes(1023)).toBe('1023 bytes');
  });

  it('scales by powers of two, with the names that go with them', () => {
    expect(formatBytes(1024)).toBe('1.0 KiB');
    expect(formatBytes(1536)).toBe('1.5 KiB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB');
  });
});

describe('lastSegment', () => {
  it('takes the part of a stored name a filesystem can hold', () => {
    // The store holds names rather than paths, but a browser reads a slash in
    // `download` as a directory it will not create.
    expect(lastSegment('fixtures/large.bin')).toBe('large.bin');
    expect(lastSegment('order.json')).toBe('order.json');
  });

  it('falls back rather than producing an empty download name', () => {
    expect(lastSegment('///')).toBe('download');
  });
});
