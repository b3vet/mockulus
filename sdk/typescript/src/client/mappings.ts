// SPDX-License-Identifier: Apache-2.0

import type { ContentMatcher, StubMapping, StubMappingImport, StubMappingList } from '../types.js';
import { encodeSegment, nullOnBodylessNotFound } from './shared.js';
import type { RequestOptions, Transport } from './transport.js';

/** Which page of the snapshot a listing wants. */
export interface ListMappingsOptions extends RequestOptions {
  /**
   * How many mappings to return, counted after `offset`. Absent means all of
   * them, which is what a deployment with a handful of stubs wants and what a
   * deployment with ten thousand does not — {@link MappingsApi.paginate} is
   * there for the second case.
   */
  limit?: number;
  /**
   * How many mappings to skip, in the snapshot's own order: priority
   * ascending, then insertion sequence descending. An offset past the end
   * answers an empty page rather than an error.
   */
  offset?: number;
}

/** How {@link MappingsApi.paginate} walks the snapshot. */
export interface PaginateMappingsOptions extends RequestOptions {
  /** How many mappings each underlying request asks for. Defaults to 100. */
  pageSize?: number;
}

/**
 * Stub mappings: the documents that say which requests match and what is
 * served back.
 *
 * Two calls here are deployment-wide and are the ones SPEC §1 tells a suite
 * sharing an instance never to press — {@link deleteAll} and {@link reset}.
 * Tagging a run's stubs with `metadata` and cleaning up through
 * {@link removeByMetadata} is what does the same job without taking another
 * team's stubs with it.
 */
export class MappingsApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Registers a stub and answers the document as stored — the document that
   * was submitted, with `id` and `uuid` stamped to the identity the server
   * assigned when none was supplied. Nothing else is rewritten.
   *
   * This is not an upsert: an `id` that already exists is refused with code
   * 109 rather than replacing the stub holding it, because on a shared
   * deployment an accidental collision would otherwise silently destroy
   * another suite's work. {@link update} is the deliberate case.
   */
  async create(mapping: StubMapping, options?: RequestOptions): Promise<StubMapping> {
    return this.transport.send<StubMapping>({
      method: 'POST',
      path: '/__admin/mappings',
      body: mapping,
      ...options,
    });
  }

  /**
   * Reads one stub by id.
   *
   * Throws on an id that names nothing, where the server answers a bare 404
   * with no body — {@link getOrNull} is the same call for a caller to whom an
   * absent stub is an ordinary answer. An id that is not a UUID at all is a
   * different statement and is refused 400 by both variants: that segment could
   * never have named a stub, so reporting it as absent would invite the caller
   * to re-derive it and ask again.
   */
  async get(id: string, options?: RequestOptions): Promise<StubMapping> {
    return this.transport.send<StubMapping>({
      method: 'GET',
      path: `/__admin/mappings/${encodeSegment(id)}`,
      ...options,
    });
  }

  /** {@link get}, answering `null` where it would throw the bodyless 404. */
  async getOrNull(id: string, options?: RequestOptions): Promise<StubMapping | null> {
    return nullOnBodylessNotFound(() => this.get(id, options));
  }

  /**
   * Lists the stubs this replica is serving.
   *
   * The listing is read from the compiled snapshot rather than from the store,
   * so it is the set of stubs this pod will actually match against — the
   * question a caller listing them has. `meta.total` counts the whole snapshot
   * rather than the page, which is how a paginating client tells when it has
   * seen everything.
   */
  async list(options?: ListMappingsOptions): Promise<StubMappingList> {
    const { limit, offset, ...rest }: ListMappingsOptions = options ?? {};
    return this.transport.send<StubMappingList>({
      method: 'GET',
      path: '/__admin/mappings',
      query: { limit, offset },
      ...rest,
    });
  }

  /**
   * Walks the whole snapshot a page at a time, yielding one stub at a time.
   *
   * A caller that wants every stub and writes the loop by hand gets the
   * termination condition wrong in one of two directions: stopping on
   * `meta.total`, which a concurrent write moves, or never stopping, because an
   * offset past the end answers an empty page rather than an error. Here the
   * only condition is a page shorter than the one asked for, which the server
   * produces exactly once — at the end — because it slices the ordered snapshot
   * rather than filtering it.
   *
   * The snapshot may change between pages. This is a stream of what was there
   * as it was read, not a transaction over it, and no admin surface offers one.
   */
  async *paginate(options?: PaginateMappingsOptions): AsyncGenerator<StubMapping, void, void> {
    const { pageSize = 100, ...rest }: PaginateMappingsOptions = options ?? {};
    if (!Number.isInteger(pageSize) || pageSize < 1) {
      // A zero or fractional page is not a smaller request, it is a loop that
      // never advances: the server treats a page size it cannot read as absent
      // and answers with everything, forever.
      throw new TypeError(`pageSize must be a positive integer, got ${String(pageSize)}`);
    }

    let offset = 0;
    for (;;) {
      const page = await this.list({ limit: pageSize, offset, ...rest });
      for (const mapping of page.mappings) {
        yield mapping;
      }
      if (page.mappings.length < pageSize) return;
      offset += page.mappings.length;
    }
  }

  /**
   * Replaces a stub in full.
   *
   * The stub's insertion sequence is preserved, so an edit does not change
   * which stub a request matches: drawing a fresh sequence would silently
   * promote the edited stub above its equal-priority peers and turn an edit
   * into a precedence change. An `id` in the body that disagrees with the path
   * is ignored — the path names the stub being replaced.
   *
   * The existence check precedes body parsing, so an invalid document against
   * an unknown id answers the bodyless 404 rather than a 422.
   */
  async update(id: string, mapping: StubMapping, options?: RequestOptions): Promise<StubMapping> {
    return this.transport.send<StubMapping>({
      method: 'PUT',
      path: `/__admin/mappings/${encodeSegment(id)}`,
      body: mapping,
      ...options,
    });
  }

  /**
   * Deletes one stub, and stops this replica serving it before the call
   * returns.
   *
   * A second delete of the same id throws the bodyless 404, because by then the
   * id names nothing. That is the server's answer rather than a choice made
   * here, and it is the one place on this surface where deleting twice is not
   * the same as deleting once — the journal's own delete is idempotent.
   */
  async delete(id: string, options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'DELETE',
      path: `/__admin/mappings/${encodeSegment(id)}`,
      accept: 'none',
      ...options,
    });
  }

  /**
   * Deletes every stub in the deployment, persistent ones included.
   *
   * Not only this replica's and not only the ephemeral ones {@link reset}
   * sweeps. On a shared deployment this destroys every other suite's stubs, and
   * the damage looks like a defect rather than like a call somebody made.
   */
  async deleteAll(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'DELETE',
      path: '/__admin/mappings',
      accept: 'none',
      ...options,
    });
  }

  /** Sweeps the stubs that did not ask to be persistent, leaving the rest. */
  async reset(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/mappings/reset',
      accept: 'none',
      ...options,
    });
  }

  /**
   * Marks every current stub persistent.
   *
   * WireMock writes its in-memory stubs out to files here. mockulus has no
   * per-node filesystem to write to, so the equivalent durable act is clearing
   * the TTL that would otherwise expire them: every current stub reads back
   * with `persistent: true` afterwards and survives {@link reset}.
   */
  async save(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/mappings/save',
      accept: 'none',
      ...options,
    });
  }

  /**
   * Loads a batch in one call, validated whole before anything is written — a
   * malformed mapping halfway through leaves the store untouched rather than
   * half-imported.
   *
   * There is **no import report**: the answer is 200 with no body at all, which
   * is why this resolves to nothing. What the batch did is in the server's log,
   * and the resulting state is one {@link list} away.
   */
  async import(batch: StubMappingImport, options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/mappings/import',
      body: batch,
      accept: 'none',
      ...options,
    });
  }

  /**
   * Finds the stubs whose `metadata` satisfies a matcher.
   *
   * The matcher is the ordinary content-matcher vocabulary — one definition of
   * what "matches" means across the product, so a matcher that works in
   * `bodyPatterns` works here unchanged. Only stubs that *have* metadata are
   * considered, which is what stops a broad matcher sweeping up every untagged
   * stub on a shared deployment.
   */
  async findByMetadata(
    matcher: ContentMatcher,
    options?: RequestOptions,
  ): Promise<StubMappingList> {
    return this.transport.send<StubMappingList>({
      method: 'POST',
      path: '/__admin/mappings/find-by-metadata',
      body: matcher,
      ...options,
    });
  }

  /**
   * Deletes the stubs whose `metadata` satisfies a matcher, and answers with
   * the documents it removed rather than a bare acknowledgement.
   *
   * This is the cleanup a suite sharing a deployment runs instead of a reset:
   * tag the run's stubs on the way in, match on the tag on the way out, and
   * nobody else's work is touched.
   */
  async removeByMetadata(
    matcher: ContentMatcher,
    options?: RequestOptions,
  ): Promise<StubMappingList> {
    return this.transport.send<StubMappingList>({
      method: 'POST',
      path: '/__admin/mappings/remove-by-metadata',
      body: matcher,
      ...options,
    });
  }
}
