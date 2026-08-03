// SPDX-License-Identifier: Apache-2.0
import type {
  LoggedRequest,
  MockulusClient,
  NearMiss,
  ServeEvent,
  StubMapping,
} from '@mockulus/admin-sdk';

/**
 * Fixtures for the journal and near-miss surfaces.
 *
 * A sibling of `lib/testing.ts` rather than an addition to it. That module is
 * shared with the stub surfaces, which a parallel stage is working in, and the
 * one thing two stages building one UI must not do is both edit the same file;
 * the two will be merged into one fixture module when the stages meet. What is
 * faked here is the transport and nothing else — the tests build a real
 * `createApi` over these clients, because the token rules live in the api and a
 * test that replaced it would be asserting against its own mock.
 */

/** The slice of the SDK client the journal and near-miss views reach for. */
export interface FakeJournalClientParts {
  requests?: Partial<MockulusClient['requests']>;
  nearMisses?: Partial<MockulusClient['nearMisses']>;
}

export function journalClient(parts: FakeJournalClientParts): MockulusClient {
  return parts as unknown as MockulusClient;
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

/** A stub as a near miss carries it: the whole document, not the two-field summary. */
export function candidateMapping(index: number, overrides: Partial<StubMapping> = {}): StubMapping {
  return {
    id: `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
    name: `candidate ${index}`,
    request: { method: 'GET', urlPath: `/api/orders/${index}` },
    response: { status: 200 },
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
