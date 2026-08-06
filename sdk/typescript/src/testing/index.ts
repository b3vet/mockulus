// SPDX-License-Identifier: Apache-2.0

/**
 * The test affordances: verification that tolerates an eventually consistent
 * journal, and a suite handle that namespaces instead of resetting.
 *
 * Everything here encodes a property of the *server* that a consumer would
 * otherwise have to discover for themselves, usually by shipping a flaky suite
 * first. There are three, they are all documented deviations from WireMock, and
 * each has a helper:
 *
 * | Property | Helper |
 * |---|---|
 * | The journal is eventually consistent — an entry is visible within `journal_flush_interval` plus index lag (deviation #10) | {@link verify} polls rather than asking once |
 * | The journal is off by default, and every verification answers 500 code 1010 until it is on (deviation #1) | {@link verify} says `journal_enabled` rather than reporting a generic failure |
 * | A deployment is one namespace, and stubs propagate across replicas within `sync_interval` (SPEC §1, deviation #11) | {@link suite} namespaces and cleans up by tag; {@link waitForStub} waits for propagation |
 *
 * They are helpers rather than a framework: each takes a client, does one
 * thing, and has no state that outlives the call except the run id a
 * {@link Suite} holds. Nothing here registers a global, hooks a test runner, or
 * needs to be installed before use, because a test affordance that has to be
 * set up is one that consumers write around.
 */

export { verify, VerificationError } from './verify.js';
export type { CountExpectation, Observation, VerifyOptions } from './verify.js';

export { suite, Suite } from './suite.js';
export type { SuiteOptions } from './suite.js';

export { waitForStub } from './wait-for-stub.js';
export type { WaitForStubOptions } from './wait-for-stub.js';

export { DEFAULT_INTERVAL_MS, DEFAULT_WITHIN_MS } from './poll.js';
export type { PollOptions } from './poll.js';
