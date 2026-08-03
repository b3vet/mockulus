// SPDX-License-Identifier: Apache-2.0

/**
 * The shared-deployment discipline of SPEC §1, as code rather than as advice.
 *
 * One mockulus deployment is **one global namespace** for stubs, scenarios and
 * the journal. That is the shape that makes it cheap to run — a Service with N
 * replicas that every CI runner can point at — and it is also the shape that
 * makes the obvious cleanup catastrophic: `POST /__admin/reset` and
 * `DELETE /__admin/mappings` do not undo *this* suite's work, they undo
 * everyone's. The damage arrives as somebody else's stub failing to match half
 * an hour later, which reads as a mockulus defect and is investigated as one.
 *
 * So the discipline is: keep your stubs distinguishable by URL, tag them with a
 * run id in `metadata`, and clean up with `remove-by-metadata`. Every part of
 * that is mechanical, which is why it belongs in a helper instead of in each
 * consumer's head. {@link suite} is the helper, and the thing it will never do,
 * under any option, is call a global reset.
 */

import { equalToJson } from '../builders/matchers.js';
import type { MockulusClient } from '../client/client.js';
import type { RequestOptions } from '../client/transport.js';
import type { ContentMatcher, StubMapping } from '../types.js';

/** How a {@link Suite} is named. */
export interface SuiteOptions {
  /**
   * A short human-readable name for the suite — `checkout`, `orders-smoke`.
   *
   * It is the readable half of the run id and of the URL namespace, so it is
   * what someone reading `GET /__admin/mappings` or a near-miss listing on a
   * shared deployment sees when they are working out whose stub they are
   * looking at. It may hold letters, digits, `.`, `_`, `~` and `-`; a leading
   * or trailing `/` is stripped and an inner one becomes a `-`, so both
   * `checkout` and `/checkout/` are accepted and mean the same thing.
   */
  prefix: string;
  /**
   * The run id, if the caller has one worth using.
   *
   * Absent, a random suffix is drawn and appended to `prefix`. Supply one only
   * when it is unique across **everything sharing the deployment at the same
   * time** — a CI job id qualifies, a CI *run* id shared by a parallel matrix
   * does not, and two suites that pick the same id will delete each other's
   * stubs in `cleanup()` while looking like they worked.
   */
  id?: string;
}

/**
 * One run's namespace on a shared deployment: a URL prefix, a metadata tag, and
 * the cleanup that removes exactly what the run registered.
 *
 * Built by {@link suite}. The identifier is the same string in both places —
 * it is the last segment of every URL the suite serves and the value of the
 * `suite` metadata key on every stub it registers — so the stubs a cleanup will
 * remove are the stubs whose URLs carry the id, and that correspondence can be
 * checked by eye in a mappings listing rather than having to be trusted.
 */
export class Suite {
  /**
   * The run id: `<prefix>-<random>`, or whatever {@link SuiteOptions.id} said.
   *
   * This is the value of the `suite` metadata key on every stub
   * {@link register} creates, and the key {@link cleanup} matches on.
   */
  readonly id: string;
  /** The URL prefix every path from {@link url} is under: `/<id>`. */
  readonly basePath: string;
  /**
   * When the suite was created.
   *
   * The journal cannot be namespaced the way stubs can — it is one log for the
   * deployment — so a verification that has to distinguish this run's traffic
   * from an earlier run's over the same URL narrows by time instead. Passing
   * this as `since` to `verify()` or to any journal read is what that looks
   * like.
   */
  readonly startedAt: Date;

  private constructor(
    private readonly client: MockulusClient,
    id: string,
  ) {
    this.id = id;
    this.basePath = `/${id}`;
    this.startedAt = new Date();
  }

  /** @internal Built through {@link suite}, which is where the naming rules are. */
  static create(client: MockulusClient, options: SuiteOptions): Suite {
    return new Suite(client, options.id ?? `${token(options.prefix)}-${randomSuffix()}`);
  }

  /**
   * A path inside this suite's namespace: `url('/orders')` is
   * `/checkout-1f4c9a2e/orders`.
   *
   * The run id is in the path rather than only in the metadata because two
   * concurrent runs of the *same* suite would otherwise register stubs on the
   * same URLs, and the second one's stub would either collide with the first's
   * or quietly win the precedence tie and serve the first run's traffic. A
   * metadata tag is enough to clean up separately; only a distinct URL is
   * enough to run at the same time.
   */
  url(path = '/'): string {
    if (path === '' || path === '/') return this.basePath;
    return path.startsWith('/') ? `${this.basePath}${path}` : `${this.basePath}/${path}`;
  }

  /**
   * Registers a stub, tagged with this suite's run id.
   *
   * The tag is added to whatever `metadata` the mapping already carried, so a
   * caller's own tags survive — `metadata: { team: 'checkout' }` becomes
   * `{ team: 'checkout', suite: '<id>' }`.
   *
   * A mapping that already carries a **different** `suite` tag is refused
   * rather than re-tagged. Overwriting it would leave the stub outside the
   * cleanup its author arranged, which is a stub left behind on a shared
   * deployment, and the caller would have no way of knowing: the create would
   * have answered 201 either way.
   */
  async register(mapping: StubMapping, options?: RequestOptions): Promise<StubMapping> {
    const existing = mapping.metadata?.['suite'];
    if (existing !== undefined && existing !== this.id) {
      throw new TypeError(
        `suite(${this.id}): this mapping is already tagged suite=${JSON.stringify(existing)}. ` +
          `Re-tagging it would take it out of the cleanup that tag belongs to; ` +
          `register it through the suite that owns it, or remove the tag.`,
      );
    }
    return this.client.mappings.create(
      { ...mapping, metadata: { ...mapping.metadata, suite: this.id } },
      options,
    );
  }

  /** The stubs this suite currently has registered, as the deployment sees them. */
  async stubs(options?: RequestOptions): Promise<StubMapping[]> {
    return (await this.client.mappings.findByMetadata(this.matcher(), options)).mappings;
  }

  /**
   * Removes exactly the stubs this suite registered, and answers with them.
   *
   * **This is not a reset, and it must never become one.** The call underneath
   * is `POST /__admin/mappings/remove-by-metadata` with a matcher over this
   * suite's own tag. `POST /__admin/reset`, `DELETE /__admin/mappings` and
   * `POST /__admin/mappings/reset` would all be shorter to write and all three
   * would delete every other runner's stubs on a shared deployment — which is
   * the exact failure this class exists to make unnecessary. If a future change
   * here is tempted by one of them, the answer is no.
   *
   * Safe to call twice, and safe to call on a suite that registered nothing:
   * `remove-by-metadata` matches only stubs that *have* metadata (deviation
   * #20), so a matcher that finds none removes none rather than sweeping the
   * untagged.
   *
   * The **journal is deliberately not touched**. There is no scoped way to
   * delete journal entries — `DELETE /__admin/requests` empties it for the
   * whole deployment — so cleaning it up here would be the global reset this
   * method refuses to be, wearing a different name. Narrow verifications with
   * {@link startedAt} instead.
   */
  async cleanup(options?: RequestOptions): Promise<StubMapping[]> {
    return (await this.client.mappings.removeByMetadata(this.matcher(), options)).mappings;
  }

  /**
   * The metadata matcher that means "this suite's stubs", used by both
   * {@link stubs} and {@link cleanup} so the set one reports is the set the
   * other deletes.
   *
   * `ignoreExtraElements` is what makes the caller's own metadata keys
   * harmless: without it the matcher would be an equality against the whole
   * metadata document and would match only stubs whose metadata is the tag and
   * nothing else.
   */
  private matcher(): ContentMatcher {
    return equalToJson({ suite: this.id }, { ignoreExtraElements: true });
  }
}

/**
 * Opens a namespace for one run on a deployment that other runs are sharing.
 *
 * ```ts
 * const run = suite(client, { prefix: 'checkout' });
 * try {
 *   await run.register(stubFor(get(run.url('/orders')).willReturn(aResponse().withStatus(200))));
 *   // … drive the system under test against run.url('/orders') …
 * } finally {
 *   await run.cleanup();
 * }
 * ```
 *
 * The `finally` is the point of the shape: a suite that throws still has to
 * clean up after itself, because the stubs it left behind outlive the process
 * that registered them — non-persistent stubs carry a 24-hour TTL rather than
 * dying with the client (deviation #3).
 */
export function suite(client: MockulusClient, options: SuiteOptions): Suite {
  return Suite.create(client, options);
}

/**
 * Turns a prefix into something that can be both a URL segment and a metadata
 * value.
 *
 * Refusing rather than escaping is deliberate. A prefix carrying a `?`, a `#`
 * or a space would either change the meaning of every URL built from it or come
 * back percent-encoded and no longer look like the name that was chosen, and
 * both of those are discovered later, in a mappings listing, by someone who did
 * not write the suite.
 */
function token(prefix: string): string {
  const trimmed = prefix.replace(/^\/+/, '').replace(/\/+$/, '').replace(/\/+/g, '-');
  if (trimmed === '' || !/^[A-Za-z0-9._~-]+$/.test(trimmed)) {
    throw new TypeError(
      `suite: \`prefix\` must be a non-empty name of letters, digits, '.', '_', '~' or '-' ` +
        `(inner '/' is folded to '-'), got ${JSON.stringify(prefix)}`,
    );
  }
  return trimmed;
}

/**
 * Eight hex characters of randomness, from the platform's CSPRNG.
 *
 * `crypto.randomUUID` rather than `Math.random` because the whole value of the
 * suffix is that two runners starting in the same second do not draw the same
 * one, and `Math.random` is seeded per process by implementations that make no
 * promise about two processes starting together. Eight characters is 2^32 of
 * space against a handful of concurrent runs sharing a prefix, which is ample,
 * and short enough that the URLs stay readable in a near-miss listing.
 *
 * A runtime with no `crypto` refuses rather than falling back: a weaker suffix
 * would be a collision nobody could see coming, and the caller can always
 * supply {@link SuiteOptions.id} themselves.
 */
function randomSuffix(): string {
  const random: Crypto | undefined = globalThis.crypto;
  if (typeof random?.randomUUID !== 'function') {
    throw new TypeError(
      'suite: this runtime has no `crypto.randomUUID` to draw a run id from. ' +
        'Pass `id` with something unique across everything sharing the deployment.',
    );
  }
  return random.randomUUID().replaceAll('-', '').slice(0, 8);
}
