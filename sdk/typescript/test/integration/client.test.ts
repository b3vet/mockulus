// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { ErrorCode, isMockulusError, MockulusClient, type StubMapping } from '../../src/index.js';
import { startServer, type Server } from './harness.js';

/**
 * The client against a real mockulus.
 *
 * The unit lane pins what goes on the wire; this pins that the server answers
 * the way the unit lane assumes. Neither is worth much alone — a stub `fetch`
 * agreeing with a client that was written from the same misreading proves only
 * that the misreading is consistent.
 *
 * Every stub registered here lives under `/sdk-it/<case>/…`, and nothing in
 * this file calls a reset. That is the discipline SPEC §1 asks of anyone
 * sharing a deployment, and it is worth following even where the deployment is
 * this file's own: a case that resets is a case that would destroy a
 * neighbour's work the day the harness starts sharing one process, and the
 * damage would show up as a failure in the neighbour rather than here.
 */
describe('the client against a live server', () => {
  let server: Server;
  let client: MockulusClient;

  beforeAll(async () => {
    server = await startServer();
    client = new MockulusClient({ baseUrl: server.adminUrl });

    // Identity before anything is derived from it. Reachability is not
    // identity, and this repository has already paid once for a probe that
    // recorded answers from a process that merely happened to hold the port.
    const version = await client.system.version();
    expect(version.guessedWireMockVersion).toBe('3.x-subset');
  });

  afterAll(async () => {
    await server?.stop();
  });

  describe('a stub mapping', () => {
    it('round-trips from create through serving to delete', async () => {
      const url = '/sdk-it/round-trip/order';

      const created = await client.mappings.create({
        request: { method: 'GET', urlPath: url },
        response: { status: 200, jsonBody: { status: 'created' } },
      });
      // The server stamps the identity when the client supplies none, and
      // echoes the document otherwise untouched — no defaults filled in.
      expect(created.id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
      expect(created.uuid).toBe(created.id);

      // Registered means being served by this replica already, without waiting
      // for a poll.
      const firstServe = await fetch(server.mockUrl + url);
      expect(firstServe.status).toBe(200);
      expect(await firstServe.json()).toEqual({ status: 'created' });

      const read = await client.mappings.get(idOf(created));
      expect(read).toEqual(created);

      const updated = await client.mappings.update(idOf(created), {
        request: { method: 'GET', urlPath: url },
        response: { status: 202, jsonBody: { status: 'updated' } },
      });
      expect(updated.id).toBe(created.id);

      const secondServe = await fetch(server.mockUrl + url);
      expect(secondServe.status).toBe(202);
      expect(await secondServe.json()).toEqual({ status: 'updated' });

      await client.mappings.delete(idOf(created));

      // The bodyless 404, flattened by the variant that exists for it.
      expect(await client.mappings.getOrNull(idOf(created))).toBeNull();
      // And the stub stops being served before the delete returns.
      expect((await fetch(server.mockUrl + url)).status).toBe(404);
    });

    it('throws rather than answering null when the id could never have named one', async () => {
      const err = await client.mappings.getOrNull('not-a-uuid').catch((e: unknown) => e);
      if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
      expect(err.status).toBe(400);
      expect(err.code).toBe(ErrorCode.Malformed);
    });
  });

  describe('a rejected mapping', () => {
    it('names every offending field by JSON pointer, in one answer', async () => {
      // Two problems in one document, both in fields this SDK's types admit —
      // the operands are what is wrong, not the shape. The server collects
      // every problem before answering, so one round of edits fixes the whole
      // mapping; a client that reported `problems[0]` would throw that away.
      const err = await client.mappings
        .create({
          request: {
            urlPattern: '/sdk-it/rejected/(unclosed',
            headers: { 'X-Trace': { matches: '(also-unclosed' } },
          },
          response: { status: 200 },
        })
        .catch((e: unknown) => e);

      if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
      expect(err.status).toBe(422);
      expect(err.has(ErrorCode.Regex)).toBe(true);
      expect(err.pointers()).toEqual(
        expect.arrayContaining(['/request/urlPattern', '/request/headers/X-Trace/matches']),
      );
      // The message is what lands in a CI log, so it has to carry the pointers
      // rather than only the status.
      expect(err.message).toContain('/request/urlPattern');
    });

    it('refuses a create over an id that is already taken', async () => {
      const id = '9c47901d-6bd5-4b7a-8896-c0ac9b8d0b0e';
      const mapping = {
        id,
        request: { method: 'GET', urlPath: '/sdk-it/duplicate/order' },
        response: { status: 200 },
      };
      await client.mappings.create(mapping);
      try {
        const err = await client.mappings.create(mapping).catch((e: unknown) => e);
        if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
        expect(err.isDuplicateStubId).toBe(true);
      } finally {
        await client.mappings.delete(id);
      }
    });
  });

  describe('the request journal, off by default', () => {
    it('answers 1010 on every journal-backed call rather than an empty result', async () => {
      // Not an empty listing and not a zero count: a verification against a
      // deployment with no journal has to fail loudly, or a suite reads "no
      // calls were made" from a server that was never recording.
      for (const call of [
        () => client.requests.list(),
        () => client.requests.unmatched(),
        () => client.requests.unmatchedNearMisses(),
        () => client.requests.count({ method: 'GET' }),
        () => client.requests.find({ method: 'GET' }),
        () => client.nearMisses.forRequestPattern({ method: 'GET' }),
      ]) {
        const err = await call().catch((e: unknown) => e);
        if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
        expect(err.isJournalDisabled).toBe(true);
        expect(err.status).toBe(500);
        expect(err.code).toBe(ErrorCode.JournalDisabled);
      }
    });

    it('does not stop near-miss scoring against the current stubs', async () => {
      // The endpoint that works on the deployment mockulus ships with, which is
      // the deployment anyone debugging a stub that will not match is standing
      // in front of. It reads the snapshot rather than the journal.
      const created = await client.mappings.create({
        request: { method: 'GET', urlPath: '/sdk-it/near-miss/orders' },
        response: { status: 200 },
      });
      try {
        const scored = await client.nearMisses.forRequest({
          method: 'GET',
          url: '/sdk-it/near-miss/order',
        });
        expect(scored.nearMisses.length).toBeGreaterThan(0);
        const closest = scored.nearMisses[0];
        expect(closest?.request.url).toBe('/sdk-it/near-miss/order');
        expect(closest?.matchResult.distance).toBeGreaterThan(0);
      } finally {
        await client.mappings.delete(idOf(created));
      }
    });
  });

  describe('listing and paginating', () => {
    it('walks an imported batch and reports a total over the whole snapshot', async () => {
      const ids = ['a', 'b', 'c', 'd', 'e'].map((n) => `4d1b0000-0000-4000-8000-00000000000${n}`);
      await client.mappings.import({
        mappings: ids.map((id, i) => ({
          id,
          request: { method: 'GET', urlPath: `/sdk-it/paginate/${i}` },
          response: { status: 200 },
          metadata: { suite: 'sdk-it-paginate' },
        })),
      });

      try {
        const walked: string[] = [];
        for await (const mapping of client.mappings.paginate({ pageSize: 2 })) {
          if (mapping.id && ids.includes(mapping.id)) walked.push(mapping.id);
        }
        expect(walked.sort()).toEqual([...ids].sort());

        const page = await client.mappings.list({ limit: 1 });
        expect(page.mappings).toHaveLength(1);
        // The total counts the snapshot, not the page, which is what lets a
        // paginating client tell when it has seen everything.
        expect(page.meta.total).toBeGreaterThanOrEqual(ids.length);
      } finally {
        // Cleanup by metadata rather than by reset — the same discipline a
        // suite sharing a deployment has to follow, dogfooded here.
        const removed = await client.mappings.removeByMetadata({
          equalToJson: { suite: 'sdk-it-paginate' },
        });
        expect(removed.mappings).toHaveLength(ids.length);
      }
    });

    it('finds stubs by their metadata and leaves untagged ones alone', async () => {
      const tagged = await client.mappings.create({
        request: { method: 'GET', urlPath: '/sdk-it/metadata/tagged' },
        response: { status: 200 },
        metadata: { suite: 'sdk-it-metadata' },
      });
      const untagged = await client.mappings.create({
        request: { method: 'GET', urlPath: '/sdk-it/metadata/untagged' },
        response: { status: 200 },
      });

      try {
        const found = await client.mappings.findByMetadata({
          equalToJson: { suite: 'sdk-it-metadata' },
        });
        expect(found.mappings.map((m) => m.id)).toEqual([tagged.id]);
      } finally {
        await client.mappings.removeByMetadata({ equalToJson: { suite: 'sdk-it-metadata' } });
        await client.mappings.delete(idOf(untagged));
      }
    });
  });

  describe('the file store', () => {
    it('round-trips bytes, and answers the same way twice on delete', async () => {
      const name = 'sdk-it/files/order-created.json';
      const text = '{"order":"created"}';
      // An `ArrayBuffer` rather than a view over one, because that is the shape
      // a client narrows wrongly and never hears about: handed to
      // `JSON.stringify` it becomes `{}`, and the upload answers 201 having
      // stored two bytes that are not the caller's. `slice()` is what makes the
      // buffer exactly the encoded length rather than whatever the encoder
      // happened to allocate.
      const buffer = new TextEncoder().encode(text).slice().buffer;

      // 201 with no body, on a create and on a replace alike.
      await client.files.put(name, buffer);
      expect(await client.files.list()).toContain(name);

      const read = await client.files.get(name);
      expect(new TextDecoder().decode(read)).toBe(text);

      await client.files.delete(name);
      // Idempotent and bodyless: the second delete answers 200 like the first,
      // because the caller's goal holds either way.
      await client.files.delete(name);
      expect(await client.files.list()).not.toContain(name);
    });

    it('uploads the window of a view, whatever kind of view it is', async () => {
      // A `DataView` is the general case and the unforgiving one: it is neither
      // an `ArrayBuffer` nor a `Uint8Array`, so a client that checked only for
      // those would JSON-encode it, and it carries an offset and a length that
      // uploading the buffer behind it would ignore. Node's Buffers are views
      // over a shared pool, so the offset is not a hypothetical.
      const name = 'sdk-it/files/slice.bin';
      const buffer = new Uint8Array([9, 1, 2, 3, 9]).slice().buffer;

      await client.files.put(name, new DataView(buffer, 1, 3));
      try {
        expect(Array.from(new Uint8Array(await client.files.get(name)))).toEqual([1, 2, 3]);
      } finally {
        await client.files.delete(name);
      }
    });

    it('throws an envelope, not a bodyless 404, for a name that is not stored', async () => {
      // This is why there is no `files.getOrNull`: the answer names the file
      // and carries a code, so flattening it to `null` would discard the only
      // part worth reading.
      const err = await client.files.get('sdk-it/files/never-uploaded').catch((e: unknown) => e);
      if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
      expect(err.status).toBe(404);
      expect(err.problems).not.toEqual([]);
      expect(err.code).toBe(ErrorCode.Malformed);
    });
  });

  describe('scenarios and settings', () => {
    it('reports the states the stubs define, and moves between them', async () => {
      const scenario = 'sdk-it-order-flow';
      const start = await client.mappings.create({
        scenarioName: scenario,
        requiredScenarioState: 'Started',
        newScenarioState: 'paid',
        request: { method: 'GET', urlPath: '/sdk-it/scenario/order' },
        response: { status: 200, body: 'unpaid' },
      });
      const paid = await client.mappings.create({
        scenarioName: scenario,
        requiredScenarioState: 'paid',
        request: { method: 'GET', urlPath: '/sdk-it/scenario/order' },
        response: { status: 200, body: 'paid' },
      });

      try {
        const listed = await client.scenarios.list();
        const found = listed.scenarios.find((s) => s.name === scenario);
        expect(found?.state).toBe('Started');
        expect(found?.possibleStates).toEqual(expect.arrayContaining(['Started', 'paid']));

        await client.scenarios.setState(scenario, 'paid');
        expect(await fetch(server.mockUrl + '/sdk-it/scenario/order').then((r) => r.text())).toBe(
          'paid',
        );

        const err = await client.scenarios
          .setState(scenario, 'refunded')
          .catch((e: unknown) => e as unknown);
        if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
        // A state no stub names is refused rather than accepted, because a
        // scenario driven somewhere nothing can match looks like a defect.
        expect(err.code).toBe(ErrorCode.InvalidScenarioState);
      } finally {
        await client.mappings.delete(idOf(start));
        await client.mappings.delete(idOf(paid));
      }
    });

    it('replaces the settings document and reads back what was written', async () => {
      const before = await client.settings.get();
      try {
        // The write answers 200 with no body, so reading it back is the only
        // way to see what was stored.
        await client.settings.update({ fixedDelay: 7 });
        expect((await client.settings.get()).settings).toEqual({ fixedDelay: 7 });

        const err = await client.settings
          .update({ nope: true } as unknown as Record<string, never>)
          .catch((e: unknown) => e);
        if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
        // An unknown key is refused by name rather than accepted and ignored —
        // a global delay that silently did not apply would look like a mock
        // server that is merely fast. The cast above is what it takes to write
        // one at all, which is the type doing its job.
        expect(err.has(ErrorCode.UnknownSetting)).toBe(true);
      } finally {
        // Replace, not merge: posting the document back is how a suite puts the
        // deployment the way it found it.
        await client.settings.update(before.settings);
      }
    });
  });

  describe('the system namespace', () => {
    it('reports health that names this replica’s store and snapshot', async () => {
      const health = await client.system.health();
      expect(health.status).toBe('healthy');
      expect(health.store.driver).toBe('memory');
      expect(health.stubs).toBeGreaterThanOrEqual(0);
      expect(health.epoch).toBeGreaterThanOrEqual(0);
    });

    it('answers the unsupported-endpoint 404 for a drain that is not enabled', async () => {
      // Disabled by default, and the route is not registered at all — so the
      // endpoint does not exist rather than existing and refusing.
      const err = await client.system.shutdown().catch((e: unknown) => e);
      if (!isMockulusError(err)) throw new Error(`expected a MockulusError, got ${String(err)}`);
      expect(err.status).toBe(404);
      expect(err.isUnsupportedEndpoint).toBe(true);
    });
  });
});

/** The id the server stamped, refusing to guess when there is not one. */
function idOf(mapping: StubMapping): string {
  if (!mapping.id) throw new Error('the server answered a stub mapping with no id');
  return mapping.id;
}
