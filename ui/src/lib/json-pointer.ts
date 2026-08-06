// SPDX-License-Identifier: Apache-2.0
import type { SyntaxNode } from '@lezer/common';
import { parser } from '@lezer/json';

/**
 * Turning a JSON Pointer into a place in a document.
 *
 * This is what makes a 422 actionable rather than merely informative. mockulus
 * refuses a bad mapping with every problem it found, each carrying the pointer
 * of the offending element (SPEC Appendix B) — and a pointer is only worth
 * printing if the reader can get to what it names. `/request/bodyPatterns/1/matchesXPath`
 * is eleven characters of instruction and a minute of counting braces; the same
 * pointer resolved to an offset is a keystroke.
 *
 * The document is parsed with the same grammar the editor highlights with —
 * `@lezer/json`, which `@codemirror/lang-json` wraps — rather than with a
 * scanner written here. Two readings of JSON in one file are two readings that
 * can disagree, and the one that disagrees silently is the one that moves the
 * cursor to the wrong field. Using the grammar also buys error recovery: a
 * document the user has edited into invalid JSON since submitting it still
 * yields a tree, and the fields that do parse are still reachable.
 *
 * Nothing here touches the DOM or CodeMirror. Offsets are the interface, and the
 * editor's job is only to select them.
 */

/** A span of the document, as character offsets. */
export interface DocumentSpan {
  readonly from: number;
  readonly to: number;
}

/**
 * What resolving a pointer produced.
 *
 * A failure carries the deepest prefix that *did* resolve, because "not in this
 * document" and "not in this document, but `/request` is" are different things
 * to a reader deciding whether they mistyped a field or the server is naming a
 * field they have not written yet.
 */
export type PointerResolution =
  | ({ readonly resolved: true } & DocumentSpan)
  | {
      readonly resolved: false;
      /** `pointer-syntax` when the pointer is not a pointer at all. */
      readonly reason: 'pointer-syntax' | 'not-in-document';
      /** The longest prefix of the pointer that named something. `''` is the document itself. */
      readonly matched: string;
    };

/**
 * The node names `@lezer/json` gives values, as opposed to the punctuation and
 * property names that share an array's or an object's child list. Membership is
 * what separates the second element of an array from the comma before it.
 */
const VALUE_NODES = new Set(['Object', 'Array', 'String', 'Number', 'True', 'False', 'Null']);

/**
 * Splits a JSON Pointer into its decoded reference tokens, or `undefined` when
 * the string is not a pointer.
 *
 * RFC 6901 requires a pointer to be empty or to start with `/`, and requires
 * `~1` to be decoded before `~0` — decoding the other way round turns the
 * escaped sequence for `~1` into a slash and silently changes which field is
 * named. The order below is that requirement and not a preference.
 */
export function parseJsonPointer(pointer: string): readonly string[] | undefined {
  if (pointer === '') {
    return [];
  }
  if (!pointer.startsWith('/')) {
    return undefined;
  }
  return pointer
    .slice(1)
    .split('/')
    .map((token) => token.replaceAll('~1', '/').replaceAll('~0', '~'));
}

/**
 * The pointer naming the first `depth` tokens of `pointer`.
 *
 * Built from the raw string rather than by re-escaping the decoded tokens: an
 * escaped token contains no `/` by definition, so the split aligns with the
 * decoded one, and re-escaping would be a second encoder to keep in step with
 * the decoder above.
 */
function prefixOf(pointer: string, depth: number): string {
  return pointer
    .split('/')
    .slice(0, depth + 1)
    .join('/');
}

/** The text of a `PropertyName` node as a string, or `undefined` if it is not one. */
function keyOf(property: SyntaxNode, text: string): string | undefined {
  const name = property.firstChild;
  if (!name || name.name !== 'PropertyName') {
    return undefined;
  }
  try {
    const decoded: unknown = JSON.parse(text.slice(name.from, name.to));
    return typeof decoded === 'string' ? decoded : undefined;
  } catch {
    // A property name that is not a complete string literal — the state a
    // document is in for as long as somebody is typing one. It names nothing
    // yet, so it matches nothing.
    return undefined;
  }
}

/** The member of an object with this key, as the whole `"name": value` property. */
function memberOf(object: SyntaxNode, key: string, text: string): SyntaxNode | undefined {
  for (let child = object.firstChild; child; child = child.nextSibling) {
    if (child.name === 'Property' && keyOf(child, text) === key) {
      return child;
    }
  }
  return undefined;
}

/** The value half of a property, which is its last child once it has one. */
function valueOf(property: SyntaxNode): SyntaxNode | undefined {
  const last = property.lastChild;
  return last && VALUE_NODES.has(last.name) ? last : undefined;
}

/**
 * The nth element of an array.
 *
 * The token has to be a canonical non-negative integer: RFC 6901 forbids
 * leading zeros, and `-` names the position after the last element, which is a
 * place to insert rather than a place that exists. Both are refusals rather than
 * near-misses, because a pointer that names no element should say so instead of
 * moving the cursor to whichever element happened to be closest.
 */
function elementOf(array: SyntaxNode, token: string): SyntaxNode | undefined {
  if (!/^(0|[1-9][0-9]*)$/.test(token)) {
    return undefined;
  }
  const wanted = Number(token);
  let index = 0;
  for (let child = array.firstChild; child; child = child.nextSibling) {
    if (!VALUE_NODES.has(child.name)) {
      continue;
    }
    if (index === wanted) {
      return child;
    }
    index += 1;
  }
  return undefined;
}

/**
 * Resolves a JSON Pointer against a document, answering the span to select.
 *
 * An object member resolves to the whole property — the name as well as the
 * value — because the reader is being sent to a *field*, and selecting only the
 * value of `"multipartPatterns": [...]` puts the cursor next to the name the
 * error message just quoted without covering it. An array element resolves to
 * the element, which has no name to include.
 */
export function resolveJsonPointer(text: string, pointer: string): PointerResolution {
  const tokens = parseJsonPointer(pointer);
  if (tokens === undefined) {
    return { resolved: false, reason: 'pointer-syntax', matched: '' };
  }

  const root = parser.parse(text).topNode.firstChild;
  if (!root || !VALUE_NODES.has(root.name)) {
    // An empty or wholly unparseable document has no root value, so even the
    // empty pointer names nothing in it.
    return { resolved: false, reason: 'not-in-document', matched: '' };
  }

  // Two cursors, because the node to descend into and the node to select are
  // not the same one: descent follows values, selection reports the property
  // that carries them.
  let container: SyntaxNode | undefined = root;
  let selected: SyntaxNode = root;

  for (const [depth, token] of tokens.entries()) {
    const missing = {
      resolved: false,
      reason: 'not-in-document',
      matched: prefixOf(pointer, depth),
    } as const;

    if (!container) {
      return missing;
    }
    if (container.name === 'Object') {
      const property = memberOf(container, token, text);
      if (!property) {
        return missing;
      }
      selected = property;
      container = valueOf(property);
      continue;
    }
    if (container.name === 'Array') {
      const element = elementOf(container, token);
      if (!element) {
        return missing;
      }
      selected = element;
      container = element;
      continue;
    }
    // A scalar cannot have members, so the pointer walked past the end of the
    // structure — which is what a pointer written against a different document
    // looks like.
    return missing;
  }

  return { resolved: true, from: selected.from, to: selected.to };
}

/**
 * Where the document first stops being JSON, for the editor to jump to when a
 * draft cannot be parsed at all.
 *
 * The position comes from the grammar rather than from `JSON.parse`'s message,
 * whose wording and whether it carries an offset at all differ between
 * JavaScript engines. `JSON.parse` still decides *whether* a document is valid —
 * it is what the server will be handed — and this only decides where to look.
 */
export function firstSyntaxError(text: string): DocumentSpan | undefined {
  const cursor = parser.parse(text).cursor();
  do {
    if (cursor.type.isError) {
      // An error node is often zero-length: it marks the point at which the
      // parser gave up rather than a run of bad characters. A one-character
      // span there is visible where an empty selection is not.
      const to = cursor.to > cursor.from ? cursor.to : Math.min(cursor.from + 1, text.length);
      return { from: cursor.from, to };
    }
  } while (cursor.next());
  return undefined;
}
