// SPDX-License-Identifier: Apache-2.0

/**
 * Waiting for a stub to become visible on the replica a client is talking to.
 *
 * mockulus is N replicas behind a Service, and a stub registered through one of
 * them is durable immediately but not yet *installed* everywhere: the pod that
 * handled the write reflects it at once, and the others converge through the
 * epoch poller within `sync_interval` — one second by default (SPEC §7.4,
 * deviation #11). A load balancer in front of the admin port makes that visible
 * from a single client, because the read after the write need not land on the
 * pod that took it.
 *
 * The failure this prevents is a bad one to debug: the stub registers, the very
 * next request to the mock port is answered by a pod that has not loaded it
 * yet, and the suite reports a matching failure against a stub that plainly
 * exists in `GET /__admin/mappings`.
 */

import type { MockulusClient } from '../client/client.js';
import type { RequestOptions } from '../client/transport.js';
import type { StubMapping } from '../types.js';
import { delay, pollBounds, type PollOptions } from './poll.js';

/** How long {@link waitForStub} waits, and what it sends while waiting. */
export type WaitForStubOptions = RequestOptions & PollOptions;

/**
 * Polls until a stub id resolves, and answers the stub.
 *
 * ```ts
 * const stub = await client.mappings.create(mapping);
 * await waitForStub(client, stub.id!);
 * ```
 *
 * The default deadline is two seconds, which is twice the default
 * `sync_interval`. A deployment that has widened `sync_interval` has to widen
 * this to match: the helper cannot read the setting, and guessing high for
 * everyone would make the common case slow to fail.
 *
 * An id that is **not a UUID at all** is refused by the server with a 400 and
 * that refusal is passed straight through rather than polled: no amount of
 * waiting turns a malformed id into a stub, and the difference between "absent"
 * and "could never have named anything" is one the caller wants to see.
 */
export async function waitForStub(
  client: MockulusClient,
  id: string,
  options: WaitForStubOptions = {},
): Promise<StubMapping> {
  const { within, interval } = pollBounds(options, 'waitForStub');
  const request: RequestOptions = {};
  if (options.signal !== undefined) request.signal = options.signal;
  if (options.headers !== undefined) request.headers = options.headers;

  const started = Date.now();
  const deadline = started + within;
  let polls = 0;

  for (;;) {
    // `getOrNull` rather than `get`, because an absent stub is the ordinary
    // answer here and the whole point of the loop. Only the *bodyless* 404 that
    // an unknown id earns becomes `null`; every other 404 on this surface
    // carries an envelope saying something else — an unsupported endpoint, a
    // deployment that is not mockulus — and is thrown.
    const stub = await client.mappings.getOrNull(id, request);
    polls += 1;
    if (stub) return stub;

    const now = Date.now();
    if (now >= deadline) {
      throw new Error(
        `waitForStub: stub ${JSON.stringify(id)} was still not visible after ` +
          `${now - started} ms and ${polls} attempt(s).\n` +
          `Cross-replica propagation is bounded by \`sync_interval\` (1 s by default, ` +
          `deviation #11), so raise \`within\` past that if this deployment has widened it. ` +
          `If the id has never existed, the create that produced it did not answer 201.`,
      );
    }
    await delay(Math.min(interval, deadline - now), options.signal);
  }
}
