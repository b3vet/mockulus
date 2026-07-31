// SPDX-License-Identifier: Apache-2.0

import { encodeFileName } from './shared.js';
import type { RequestOptions, Transport } from './transport.js';

/**
 * What a file upload accepts. Bytes in any of the shapes a caller is likely to
 * be holding one in — a `Buffer` read off disk, an `ArrayBuffer` from another
 * `fetch`, a typed-array view over part of a larger buffer, or a string.
 */
export type FileBody = string | ArrayBuffer | ArrayBufferView;

/** How a file upload is sent. */
export interface PutFileOptions extends RequestOptions {
  /**
   * The `Content-Type` to send. Defaults to `application/octet-stream`. The
   * server stores the body without interpretation and never reads this, so it
   * exists for a proxy or a gateway in front of the deployment that does.
   */
  contentType?: string;
}

/**
 * The response-body file store, which backs `bodyFileName`.
 *
 * A body can be uploaded once and referenced by many stubs. Uploading is
 * independent of the stubs that reference it in both directions: registering a
 * stub before its file exists is legal and the reference resolves when the file
 * arrives, and deleting a file leaves the stubs referencing it serving code
 * 1022 rather than failing to load — one deleted file does not take a
 * deployment's stubs down with it.
 */
export class FilesApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Lists every stored file name.
   *
   * The one listing on this surface that answers a **bare JSON array** rather
   * than an envelope, so there is nothing to unwrap. A name may contain
   * slashes, since the store holds names rather than paths.
   */
  async list(options?: RequestOptions): Promise<string[]> {
    return this.transport.send<string[]>({
      method: 'GET',
      path: '/__admin/files',
      ...options,
    });
  }

  /**
   * Downloads a file's bytes, verbatim.
   *
   * Files hold arbitrary response bodies, so the server does not guess a media
   * type from the name and neither does this: the bytes come back as an
   * `ArrayBuffer` and the caller decodes them knowing what they put there.
   *
   * An unknown name throws. Unlike the bodyless 404 an unknown stub id gets,
   * this one carries the error envelope and names the file that was asked for,
   * so there is nothing to gain from flattening it into `null` — hence no
   * `getOrNull` here.
   */
  async get(name: string, options?: RequestOptions): Promise<ArrayBuffer> {
    return this.transport.send<ArrayBuffer>({
      method: 'GET',
      path: `/__admin/files/${encodeFileName(name)}`,
      accept: 'bytes',
      ...options,
    });
  }

  /**
   * Stores a file under a name, replacing anything already there.
   *
   * The replica that handles the upload serves the new bytes on the very next
   * request rather than at its next poll, so a test that uploads a body and
   * immediately exercises the stub referencing it does not race.
   *
   * The answer is **201 with no body**, on a create and on a replace alike,
   * which is why this resolves to nothing.
   */
  async put(name: string, body: FileBody, options?: PutFileOptions): Promise<void> {
    const { contentType = 'application/octet-stream', ...rest }: PutFileOptions = options ?? {};
    await this.transport.send<void>({
      method: 'PUT',
      path: `/__admin/files/${encodeFileName(name)}`,
      body: asBytes(body),
      contentType,
      accept: 'none',
      ...rest,
    });
  }

  /**
   * Deletes a file.
   *
   * Idempotent, and bodyless: a name that is not stored answers 200 like any
   * other, because the caller's goal — that name not being in the store — holds
   * either way. Only a name the store could never hold is refused, with a 422.
   */
  async delete(name: string, options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'DELETE',
      path: `/__admin/files/${encodeFileName(name)}`,
      accept: 'none',
      ...options,
    });
  }
}

/**
 * Narrows an upload body to something the transport sends as bytes.
 *
 * This exists because the failure it prevents is silent. The transport sends a
 * string or a `Uint8Array` as the body and JSON-encodes anything else, so an
 * `ArrayBuffer` handed straight through would be serialized as `{}` — a
 * successful 201 storing two bytes that are not the caller's.
 */
function asBytes(body: FileBody): string | Uint8Array {
  if (typeof body === 'string') return body;
  if (body instanceof ArrayBuffer) return new Uint8Array(body);
  // A view over part of a larger buffer keeps its window: uploading a
  // `Buffer.subarray` should send the slice the caller took, not the pool it
  // came out of, and Node's Buffers routinely are such views.
  return new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
}
