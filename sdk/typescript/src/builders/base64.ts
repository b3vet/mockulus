// SPDX-License-Identifier: Apache-2.0

/**
 * Reading the two operands that reach the server base64-encoded.
 *
 * `binaryEqualTo` and `base64Body` are decoded by one function on the server —
 * `matchers.DecodeBase64`, which the response body's parser deliberately shares
 * with the matcher's — because two spellings of "what counts as base64" would
 * drift and the pair would then disagree about which stubs register while
 * looking identical in review. The same argument applies on this side, so both
 * builders read their operand here.
 */

/**
 * The standard alphabet with optional padding.
 *
 * Padding is optional in Java's decoder, and the server follows it, so the
 * unpadded spelling anything calling Java's raw encoder produces is accepted.
 * The url-safe alphabet's `-` and `_` are refused, as are whitespace and
 * embedded newlines, which is what the server refuses too.
 */
const base64Shape = /^[A-Za-z0-9+/]*={0,2}$/;

/**
 * Reads an operand that must reach the server as base64.
 *
 * Bytes are encoded, because base64 is a transport spelling and not something a
 * caller should have to produce correctly. A string is taken as already encoded
 * and checked here rather than at registration, so a typo in a fixture is a
 * `TypeError` at the call site instead of a 422 from the server — which is the
 * whole point of this layer, applied to a value the type system cannot inspect.
 *
 * @param field The builder the operand was given to, so the message names the
 * call that has to change rather than the helper that noticed.
 */
export function asBase64(value: Uint8Array | ArrayBuffer | string, field: string): string {
  if (typeof value !== 'string') {
    return encode(value instanceof Uint8Array ? value : new Uint8Array(value));
  }
  // A trailing unit of one character cannot hold a byte, and is refused by the
  // padded and unpadded readings alike — so it is refused here rather than
  // reaching a decoder that would answer with a length error naming nothing the
  // caller wrote.
  const unpadded = value.replace(/=+$/, '');
  if (!base64Shape.test(value) || unpadded.length % 4 === 1) {
    throw new TypeError(`${field} was given a string that is not base64: ${JSON.stringify(value)}`);
  }
  return value;
}

/**
 * Base64-encodes bytes without Node's `Buffer`, which this package does not
 * depend on: one build runs in Node, in a browser and in a bundler's output.
 *
 * The bytes are handed to `btoa` a window at a time. `String.fromCharCode` takes
 * them as arguments, and a megabyte payload passed in one call is an argument
 * list long enough to overflow the stack on every engine — a failure that only
 * appears once a fixture grows, which is the worst time to find it.
 */
function encode(bytes: Uint8Array): string {
  const window = 0x8000;
  let latin1 = '';
  for (let offset = 0; offset < bytes.length; offset += window) {
    latin1 += String.fromCharCode(...bytes.subarray(offset, offset + window));
  }
  return btoa(latin1);
}
