// SPDX-License-Identifier: Apache-2.0
import { MockulusError, type MockulusClient, type StubMapping } from '@mockulus/admin-sdk';

/**
 * Fixtures the unit tests share.
 *
 * The tests build a real {@link createApi} over a fake client rather than a
 * fake api over nothing: the token rules — where the value is kept, when the
 * sheet opens, what is re-run afterwards — live in the api, and a test that
 * replaced it would be asserting against its own mock. What is faked here is
 * only the transport, which is where the network would be.
 */

/** The slice of the SDK client a test needs, without the six namespaces it does not. */
export interface FakeClientParts {
  mappings?: Partial<MockulusClient['mappings']>;
  system?: Partial<MockulusClient['system']>;
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
