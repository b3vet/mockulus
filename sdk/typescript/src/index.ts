// SPDX-License-Identifier: Apache-2.0

/**
 * `@mockulus/admin-sdk` — a typed client for the mockulus admin API.
 *
 * The package's compatibility claim is the server's own: it types the
 * **supported subset** and nothing more, so a stub this SDK can express is a
 * stub the server registers. That moves mockulus' fail-loud contract from a 422
 * at registration to a type error before the call is written.
 *
 * The client, the WireMock-style builders and the test helpers land over the
 * releases that follow. What is here today is the part of the contract that
 * needs no client to be true.
 */

export { ErrorCode, ErrorCodeStatus } from './codes.js';
export type { ErrorCodeValue } from './codes.js';
