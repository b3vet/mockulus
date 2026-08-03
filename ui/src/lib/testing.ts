// SPDX-License-Identifier: Apache-2.0
import {
  MockulusError,
  type Health,
  type LoggedRequest,
  type MockulusClient,
  type NearMiss,
  type Scenario,
  type ServeEvent,
  type StubMapping,
  type VersionInfo,
} from '@mockulus/admin-sdk';

/**
 * Fixtures the unit tests share.
 *
 * The tests build a real {@link createApi} over a fake client rather than a
 * fake api over nothing: the token rules — where the value is kept, when the
 * sheet opens, what is re-run afterwards — live in the api, and a test that
 * replaced it would be asserting against its own mock. What is faked here is
 * only the transport, which is where the network would be.
 *
 * This was three modules while three stages were building three parts of one UI
 * at once, because the file two concurrent stages must not both edit is the one
 * they share. That reason expired when the stages met, and it left three
 * spellings of the same idea behind: `fakeClient`, `journalClient` and
 * `panelClient` were the same cast over three overlapping interfaces, so which
 * one a test reached for said nothing except which stage wrote it.
 */

/**
 * The slice of the SDK client a test needs.
 *
 * Every namespace is optional because no view calls all of them, and a test
 * that supplied the ones it never reaches would be describing a server rather
 * than the two or three answers its assertions turn on. A call into a namespace
 * a test left out throws, which is the honest outcome: the test asked for
 * something it never said the server would answer.
 */
export interface FakeClientParts {
  mappings?: Partial<MockulusClient['mappings']>;
  system?: Partial<MockulusClient['system']>;
  requests?: Partial<MockulusClient['requests']>;
  nearMisses?: Partial<MockulusClient['nearMisses']>;
  scenarios?: Partial<MockulusClient['scenarios']>;
  files?: Partial<MockulusClient['files']>;
  settings?: Partial<MockulusClient['settings']>;
}

export function fakeClient(parts: FakeClientParts): MockulusClient {
  return parts as unknown as MockulusClient;
}

/** An error envelope as the server would have sent it, for the states that render one. */
export function adminError(
  status: number,
  problems: { code: number; title?: string; detail?: string; source?: { pointer?: string } }[],
  init: { method?: string; path?: string } = {},
): MockulusError {
  return new MockulusError({
    status,
    method: init.method ?? 'GET',
    path: init.path ?? '/__admin/mappings',
    problems,
  });
}

/** A stub mapping with an id, since every document the server answers with has one. */
export function stubMapping(index: number, overrides: Partial<StubMapping> = {}): StubMapping {
  return {
    id: `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
    name: `stub ${index}`,
    request: { method: 'GET', urlPath: `/api/things/${index}` },
    response: { status: 200 },
    ...overrides,
  };
}

/** `count` mappings, for the cases about paging and about not rendering them all. */
export function stubMappings(count: number, overrides: Partial<StubMapping> = {}): StubMapping[] {
  return Array.from({ length: count }, (_, index) => stubMapping(index + 1, overrides));
}

/**
 * A stub as a near miss carries it: the whole document, not the two-field
 * summary. Kept apart from {@link stubMapping} because the URL it describes is
 * the one the near-miss request fixtures below are scored against, and a
 * candidate whose path had nothing to do with the request would make every
 * difference in a test's expected output arbitrary.
 */
export function candidateMapping(index: number, overrides: Partial<StubMapping> = {}): StubMapping {
  return {
    id: `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
    name: `candidate ${index}`,
    request: { method: 'GET', urlPath: `/api/orders/${index}` },
    response: { status: 200 },
    ...overrides,
  };
}

/** A recorded request, with every member the server always sends. */
export function loggedRequest(overrides: Partial<LoggedRequest> = {}): LoggedRequest {
  return {
    method: 'GET',
    url: '/api/orders',
    headers: {},
    cookies: {},
    queryParams: {},
    body: '',
    loggedDateString: '2026-07-29T09:15:00.000Z',
    loggedDate: Date.parse('2026-07-29T09:15:00.000Z'),
    ...overrides,
  };
}

/**
 * A journal entry. Matched by default, since most traffic is: the unmatched
 * cases say so explicitly, which is also how they read in the tests.
 */
export function serveEvent(index: number, overrides: Partial<ServeEvent> = {}): ServeEvent {
  const matched = overrides.wasMatched ?? true;
  return {
    id: `2VZ8mQ0kZ9pQnR6b1cN3sT7yW${String(index).padStart(2, '0')}`,
    request: loggedRequest({ url: `/api/orders/${index}` }),
    responseDefinition: { status: matched ? 200 : 404 },
    wasMatched: matched,
    ...(matched ? { stubMapping: { id: `stub-${index}`, name: `stub ${index}` } } : {}),
    ...overrides,
  };
}

/** One request-and-stub pairing, with the distance and differences between them. */
export function nearMiss(overrides: Partial<NearMiss> = {}): NearMiss {
  return {
    request: loggedRequest(),
    stubMapping: candidateMapping(1),
    matchResult: {
      distance: 0.25,
      differences: [{ kind: 'url', expected: '/api/orders/1', actual: '/api/orders' }],
    },
    ...overrides,
  };
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
