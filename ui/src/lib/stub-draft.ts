// SPDX-License-Identifier: Apache-2.0
import type { StubMapping } from '@mockulus/admin-sdk';

/**
 * The document the editor edits, and the rules for getting into and out of it.
 *
 * The editor's subject is the mapping JSON itself rather than a form over it.
 * That is the whole design: the admin API's stub schema is what the docs, the
 * spec and every WireMock example are written in, so a user who knows what they
 * want can paste it, and a user who does not gets the same 422 the API would
 * have given them, pointing at the same field. A form would have to grow a
 * control per supported key and would silently drop anything it had not grown
 * one for — the one failure mode this project refuses everywhere else.
 *
 * Nothing here is reactive or knows about the network.
 */

/** Which of the three ways into the editor a page was opened by. */
export type EditorMode = 'create' | 'edit' | 'duplicate';

/**
 * Reads the mode out of the route path.
 *
 * The mode lives in the URL rather than in component state because all three
 * are linkable: "the editor, on this stub" is a thing to bookmark and to send
 * to a colleague, and a mode held in a variable would be lost on the reload
 * that the SPA fallback exists to make work.
 */
export function editorModeOf(routePath: string): EditorMode | undefined {
  const segments = routePath.split('/').filter((segment) => segment !== '');
  if (segments[0] !== 'stubs') {
    return undefined;
  }
  if (segments.length === 2 && segments[1] === 'new') {
    return 'create';
  }
  if (segments.length === 3 && segments[2] === 'edit') {
    return 'edit';
  }
  if (segments.length === 3 && segments[2] === 'duplicate') {
    return 'duplicate';
  }
  return undefined;
}

/** Two spaces, matching what `StubDetail` renders and what the SDK's examples use. */
const INDENT = 2;

/**
 * What a new stub starts as.
 *
 * A working stub rather than `{}`: pressing Save without touching it registers
 * something that serves, which makes the round trip — write, then watch it match
 * — available before the user has learned the schema. The fields chosen are the
 * four every mapping has an opinion about, so the shape of the document is
 * visible without reading the docs first.
 */
export const NEW_STUB_TEMPLATE: string = `${JSON.stringify(
  {
    request: { method: 'GET', urlPath: '/example' },
    response: {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
      jsonBody: { hello: 'world' },
    },
  },
  null,
  INDENT,
)}\n`;

/** The name a duplicate starts with, so two stubs are never indistinguishable in the list. */
export function duplicateNameOf(name: string | undefined): string {
  return name === undefined || name.trim() === '' ? 'Copy' : `${name} (copy)`;
}

/**
 * The document to open the editor on, given the stub it was opened from.
 *
 * An edit shows the mapping exactly as the server stores it, identity included:
 * that is what `GET /__admin/mappings/{id}` answered and what the reader saw on
 * the detail page, and hiding a field the round trip will carry anyway would
 * make the editor a different document from the one being edited.
 *
 * A duplicate has its identity stripped instead. Leaving `id` in would make the
 * first Save a create against an id that already exists — refused with code 109,
 * which is the server correctly reporting a mistake this side had every means to
 * avoid. `uuid` goes with it because the two are one identity under two
 * spellings.
 */
export function draftFor(mapping: StubMapping, mode: EditorMode): string {
  if (mode !== 'duplicate') {
    return `${JSON.stringify(mapping, null, INDENT)}\n`;
  }
  const copy: StubMapping = { ...mapping, name: duplicateNameOf(mapping.name) };
  delete copy.id;
  delete copy.uuid;
  return `${JSON.stringify(copy, null, INDENT)}\n`;
}

/** A draft that parsed, or the reason it did not. */
export type DraftParse =
  | { readonly ok: true; readonly mapping: StubMapping }
  | { readonly ok: false; readonly message: string };

/**
 * Reads the editor's text as a stub mapping.
 *
 * The only two things checked here are the two the server cannot report usefully
 * on this side of the wire: whether the text is JSON at all, and whether it is a
 * JSON *object*. Everything else — unknown fields, unsupported matchers,
 * regexes that do not compile — is deliberately left to the server, because the
 * server is the authority on what it accepts and a second opinion held here
 * would either be a copy of its rules that goes stale or a stricter set that
 * refuses documents it would have taken.
 */
export function parseDraft(text: string): DraftParse {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : String(err) };
  }
  if (parsed === null || typeof parsed !== 'object') {
    return {
      ok: false,
      message: `A stub mapping is a JSON object; this document is ${describeJson(parsed)}.`,
    };
  }
  if (Array.isArray(parsed)) {
    return {
      ok: false,
      message:
        'A stub mapping is a JSON object; this document is an array. To load several ' +
        'mappings at once, use Import on the stub list.',
    };
  }
  return { ok: true, mapping: parsed as StubMapping };
}

/** How to name a value that was not the object we needed, for a message worth reading. */
export function describeJson(value: unknown): string {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'an array';
  switch (typeof value) {
    case 'string':
      return 'a string';
    case 'number':
      return 'a number';
    case 'boolean':
      return 'a boolean';
    default:
      return 'an object';
  }
}
