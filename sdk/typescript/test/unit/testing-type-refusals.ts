// SPDX-License-Identifier: Apache-2.0

/**
 * The verifications this layer refuses to describe, asserted as type errors.
 *
 * Not a vitest case, and the file extension says so — there is nothing to run.
 * Every assertion is a `@ts-expect-error`, and what checks them is
 * `pnpm run check`, which type-checks `test/` alongside `src/`. A directive that
 * stops being needed is itself an error, so these cannot rot into silence.
 *
 * The one refusal here is worth the file. `{ times: 2, atLeast: 1 }` is not a
 * stricter verification, it is two of them, and any implementation that accepted
 * it would have to pick one and discard the other — the accept-and-ignore
 * failure the whole project is built to refuse. The runtime check in `verify`
 * exists as well, for the JavaScript consumers this package ships to; this is
 * the half that never has to run.
 */

import { verify, type MockulusClient } from '../../src/index.js';

declare const client: MockulusClient;

/** The two expectations are mutually exclusive rather than merely discouraged. */
export async function oneExpectationAtATime(): Promise<void> {
  await verify(client, { method: 'GET' }, { times: 2 });
  await verify(client, { method: 'GET' }, { atLeast: 2 });
  await verify(client, { method: 'GET' }, { within: 5_000, interval: 250 });

  // @ts-expect-error `times` is exactly that many and `atLeast` is that many or
  // more; a verification that carried both would be honouring one of them.
  await verify(client, { method: 'GET' }, { times: 2, atLeast: 1 });
}
