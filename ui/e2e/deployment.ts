// SPDX-License-Identifier: Apache-2.0

/**
 * Where the deployment under test is, and how it is configured.
 *
 * Read by the Playwright config, by the global setup and by the cases, so the
 * three cannot disagree about which server they are talking to. Everything is
 * overridable from the environment because the nightly lane and a developer's
 * machine start the binary the same way but do not necessarily have the same
 * ports free.
 */

/**
 * The ports.
 *
 * Explicit and unusual on purpose. A suite bound to 8080/9090 would find
 * whatever a developer happened to be running, and — since a mock server's
 * whole job is to answer plausibly — it would get plausible answers back. The
 * pairing is kept adjacent so that one is obviously the other's admin side.
 */
const MOCK_PORT = process.env.MOCKULUS_UI_E2E_MOCK_PORT ?? '18493';
const ADMIN_PORT = process.env.MOCKULUS_UI_E2E_ADMIN_PORT ?? '18593';

/**
 * The admin token this run configures, or the empty string for a deployment
 * that asks for none.
 *
 * Both are real configurations that real deployments use, and they take
 * different paths through the UI: without a token nothing ever 401s and the
 * sheet must never appear, and with one the first call 401s and everything
 * afterwards depends on a header. The suite is therefore run twice rather than
 * covering one and asserting about the other.
 */
export function e2eToken(): string {
  return process.env.MOCKULUS_UI_E2E_TOKEN ?? '';
}

export function adminBaseURL(): string {
  return process.env.MOCKULUS_UI_E2E_ADMIN_URL ?? `http://127.0.0.1:${ADMIN_PORT}`;
}

export function mockBaseURL(): string {
  return process.env.MOCKULUS_UI_E2E_MOCK_URL ?? `http://127.0.0.1:${MOCK_PORT}`;
}

/** Where the admin mux serves the UI. The build's `base` and this must agree. */
export const UI_PATH = '/__admin/mockulus/ui/';

/** The key the UI keeps the token under, asserted directly by the token cases. */
export const TOKEN_STORAGE_KEY = 'mockulus.admin-token';

/**
 * The environment the binary is started with.
 *
 * The journal is switched on because the round-trip case needs it, and because
 * off is the default that every other lane already exercises: `journal_enabled`
 * is false out of the box (SPEC §2, pay-per-use), the UI's journal-off empty
 * state has unit coverage, and a browser suite that could only ever see that
 * state would never reach the create → serve → journal path U9 asks for.
 */
export function e2eEnv(): Record<string, string> {
  const token = e2eToken();
  return {
    MOCKULUS_PORT: MOCK_PORT,
    MOCKULUS_ADMIN_PORT: ADMIN_PORT,
    MOCKULUS_JOURNAL_ENABLED: 'true',
    MOCKULUS_LOG_LEVEL: 'warn',
    ...(token === '' ? {} : { MOCKULUS_ADMIN_AUTH_TOKEN: token }),
  };
}

/** The headers an admin call needs on this run, which is none when no token is set. */
export function adminHeaders(): Record<string, string> {
  const token = e2eToken();
  return token === '' ? {} : { Authorization: `Token ${token}` };
}
