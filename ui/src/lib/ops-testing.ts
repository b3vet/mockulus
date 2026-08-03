// SPDX-License-Identifier: Apache-2.0
import type { Health, MockulusClient, Scenario, VersionInfo } from '@mockulus/admin-sdk';

/**
 * Fixtures for the scenarios and ops panels.
 *
 * These panels reach five of the client's namespaces between them, where
 * `lib/testing.ts` fakes the two the stub views need. The two files exist
 * separately rather than one growing to cover everything because they are
 * developed by different stages against different surfaces, and a shared
 * fixture file is the one place two such stages would collide over a change
 * neither of them needs.
 *
 * What is faked here is only the transport, as there: the tests build a real
 * `createApi` over these, so the token rules under test are the real ones.
 */

/** The slice of the SDK client these panels call, without the ones they do not. */
export interface PanelClientParts {
  mappings?: Partial<MockulusClient['mappings']>;
  system?: Partial<MockulusClient['system']>;
  scenarios?: Partial<MockulusClient['scenarios']>;
  files?: Partial<MockulusClient['files']>;
  settings?: Partial<MockulusClient['settings']>;
  requests?: Partial<MockulusClient['requests']>;
}

export function panelClient(parts: PanelClientParts): MockulusClient {
  return parts as unknown as MockulusClient;
}

/** A scenario as the listing answers one: identity is the name, and `Started` is always possible. */
export function scenario(
  name: string,
  state: string,
  possibleStates: readonly string[] = ['Started'],
): Scenario {
  const states = new Set<string>(['Started', ...possibleStates, state]);
  return { id: name, name, state, possibleStates: [...states].sort() };
}

/** A health document, defaulted to what an ordinary single-replica deployment answers. */
export function health(overrides: Partial<Health> = {}): Health {
  return {
    status: 'healthy',
    message: 'mockulus is ok',
    version: '1.1.0',
    uptimeInSeconds: 61,
    timestamp: '2026-07-29T10:00:00Z',
    store: { driver: 'memory' },
    stubs: 3,
    epoch: 7,
    ...overrides,
  };
}

/** A version document, including the surface claim that identifies the server. */
export function versionInfo(overrides: Partial<VersionInfo> = {}): VersionInfo {
  return {
    version: '1.1.0',
    guessedWireMockVersion: '3.x-subset',
    goVersion: 'go1.25.0',
    ...overrides,
  };
}
