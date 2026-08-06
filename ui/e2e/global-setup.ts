// SPDX-License-Identifier: Apache-2.0
import { request } from '@playwright/test';
import { adminBaseURL, adminHeaders, e2eToken } from './deployment';

/**
 * Asks the server what it is before a single case records anything from it.
 *
 * Reachability is not identity, and this project has already paid for the
 * difference once: a stray mockulus bound to the port a WireMock container had
 * published answered every probe plausibly, and only `GET /__admin/version`
 * naming the wrong server caught it (AGENTS.md, "Probing an external
 * reference", rule 1). Playwright's own `webServer.url` check is a liveness
 * probe — it stops waiting as soon as *something* answers 200 — so on its own
 * it would adopt whatever was already listening.
 *
 * The tell is `guessedWireMockVersion`. It is a mockulus extension to the
 * version document and the field the E2E harness uses in the other direction to
 * refuse a service claiming to be WireMock, so requiring it here is the same
 * check read the other way round. Comparing the reported version against
 * anything is deliberately not done: a binary built from a working tree reports
 * a `-dirty` describe string, and a comparison that fails for that reason would
 * teach whoever met it to delete the check.
 *
 * The probe carries the run's token, so on the token run this doubles as
 * evidence that the token the server was started with is the token the suite
 * holds — a mismatch would otherwise surface much later, as a token case
 * failing for a reason that looks like a UI defect.
 */
export default async function globalSetup(): Promise<void> {
  const api = await request.newContext({ baseURL: adminBaseURL() });
  try {
    const response = await api.get('/__admin/version', { headers: adminHeaders() });
    if (!response.ok()) {
      throw new Error(
        `GET /__admin/version answered ${response.status()} at ${adminBaseURL()}. ` +
          (e2eToken() === ''
            ? 'This run configured no admin token, so the admin API should answer.'
            : 'This run configured an admin token; a 401 means the server is not holding the same one.'),
      );
    }

    const body: unknown = await response.json();
    const document = body as { version?: unknown; guessedWireMockVersion?: unknown };
    if (typeof document.guessedWireMockVersion !== 'string') {
      throw new Error(
        `Whatever is listening at ${adminBaseURL()} is not mockulus: its version document has no ` +
          `guessedWireMockVersion. Got ${JSON.stringify(body)}.`,
      );
    }
    if (typeof document.version !== 'string' || document.version === '') {
      throw new Error(
        `The server at ${adminBaseURL()} reported no version: ${JSON.stringify(body)}`,
      );
    }

    process.stdout.write(
      `mockulus ${document.version} (WireMock surface ${document.guessedWireMockVersion}) ` +
        `at ${adminBaseURL()}, admin token ${e2eToken() === '' ? 'not configured' : 'configured'}\n`,
    );
  } finally {
    await api.dispose();
  }
}
