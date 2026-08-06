// SPDX-License-Identifier: Apache-2.0
import { expect as playwrightExpect, request } from '@playwright/test';
import { adminBaseURL, adminHeaders, mockBaseURL } from './deployment';
import { expect, seedToken, test, uiUrl } from './harness';

/**
 * Create → serve → journal: the third of U9's three, and the only case in this
 * repository that crosses all of it.
 *
 * A stub is registered *through the editor* — typed into CodeMirror and saved
 * with the button, not posted with a client — because everything interesting
 * happens between the two: the document leaves the editor as text, the SDK
 * turns it into a request, the admin mux compiles it into the snapshot. Then
 * the mock port is driven from outside the browser, and the journal in the UI
 * is asked whether it saw it. Each leg is covered elsewhere; nothing else joins
 * them up.
 *
 * The path carries a prefix of its own so that a deployment which is not
 * exclusively this suite's — this one is, but the discipline is the project's
 * (AGENTS.md, rule 7) — is namespaced rather than reset. Nothing here deletes
 * anything.
 */
const STUB_PATH = '/e2e/ui-smoke/orders';
const STUB_BODY = 'served by the stub the browser created';

/** One line, so the editor's indent-on-input has no newline to react to. */
const MAPPING = JSON.stringify({
  name: 'ui smoke',
  request: { method: 'GET', urlPath: STUB_PATH },
  response: { status: 201, body: STUB_BODY },
});

test.describe('a stub created in the browser', () => {
  test.beforeEach(async ({ page }) => {
    await seedToken(page);
  });

  test('is compiled into the snapshot, served, and recorded in the journal', async ({
    page,
    problems,
  }) => {
    await page.goto(uiUrl('stubs/new'));

    const editor = page.getByRole('textbox', { name: 'Stub mapping JSON' });
    await expect(editor).toBeVisible();

    // Select the template and type over it. Going through the editor rather
    // than around it is the point of doing this in a browser at all: the
    // keymap, the document model and the two-way binding are all on the path
    // between a key press and the request body.
    await editor.click();
    await page.keyboard.press('ControlOrMeta+a');
    await page.keyboard.type(MAPPING);

    await page.getByRole('button', { name: 'Create stub' }).click();

    // The editor navigates to the stub as the server now holds it, so arriving
    // on a detail page is the confirmation that the write took — and the id in
    // the URL is the identity the server assigned.
    await expect(page.getByText(STUB_PATH).first()).toBeVisible();
    const stubId = new URL(page.url()).pathname.split('/').pop();
    playwrightExpect(stubId).toBeTruthy();

    // Serve it. From outside the browser, against the mock port: the page is on
    // the admin origin, and asking it to fetch another one would be testing the
    // browser's cross-origin rules rather than the mock.
    const mock = await request.newContext({ baseURL: mockBaseURL() });
    try {
      const served = await mock.get(STUB_PATH);
      playwrightExpect(served.status()).toBe(201);
      playwrightExpect(await served.text()).toBe(STUB_BODY);
    } finally {
      await mock.dispose();
    }

    // The journal is written off the hot path — a queue drained by flush workers
    // on `journal_flush_interval` — so a request that has been answered is not
    // yet a request that has been recorded. Waiting for the deployment to have
    // flushed it, before opening the page, keeps this case about what the UI
    // renders rather than about how fast the writers are: the journal view reads
    // once when it mounts and would otherwise be asserted against an empty
    // answer it was right to give.
    const admin = await request.newContext({ baseURL: adminBaseURL() });
    try {
      await playwrightExpect
        .poll(
          async () => {
            const journal = await admin.get('/__admin/requests?limit=50', {
              headers: adminHeaders(),
            });
            if (!journal.ok()) {
              return [];
            }
            const body = (await journal.json()) as { requests: { request: { url: string } }[] };
            return body.requests.map((entry) => entry.request.url);
          },
          { timeout: 15_000, message: `the journal never recorded ${STUB_PATH}` },
        )
        .toContain(STUB_PATH);
    } finally {
      await admin.dispose();
    }

    // And now the journal, in the UI, reading through the SDK with whatever
    // credential this run configured.
    await page.goto(uiUrl('journal'));
    await expect(page.getByRole('heading', { level: 1, name: 'Journal' })).toBeVisible();

    const entries = page.getByRole('list', { name: 'Journal entries' });
    await expect(entries.getByText(STUB_PATH).first()).toBeVisible();

    // Matched, not merely present. An unmatched entry for the same URL would
    // mean the stub was written and then not compiled into the snapshot, which
    // is a different defect wearing the same evidence.
    await page.getByRole('tab', { name: /^Matched/ }).click();
    await expect(entries.getByText(STUB_PATH).first()).toBeVisible();

    expect(problems.cspViolations).toEqual([]);
    expect(problems.pageErrors).toEqual([]);
  });

  test('is refused with pointered problems when the server will not have it', async ({ page }) => {
    await page.goto(uiUrl('stubs/new'));

    const editor = page.getByRole('textbox', { name: 'Stub mapping JSON' });
    await expect(editor).toBeVisible();
    await editor.click();
    await page.keyboard.press('ControlOrMeta+a');
    // A matcher mockulus does not implement, which is the refusal the whole
    // error surface was built for: a 422 naming the offending JSON pointer,
    // rendered against the document rather than as a toast.
    await page.keyboard.type(
      JSON.stringify({
        request: {
          method: 'GET',
          urlPath: STUB_PATH,
          headers: { 'X-Trace': { matchesXPath: '/a' } },
        },
        response: { status: 200 },
      }),
    );

    await page.getByRole('button', { name: 'Create stub' }).click();

    await expect(
      page.getByRole('heading', {
        name: 'The server refused this mapping, and nothing was written',
      }),
    ).toBeVisible();
    const problems = page.getByRole('list', { name: 'Problems the server reported' });
    await expect(problems.getByRole('button', { name: /Go to/ }).first()).toBeVisible();

    // Nothing was written: the deployment still refuses the URL, since the only
    // stub naming it is the valid one from the case above and this document
    // never landed.
    const admin = await request.newContext({ baseURL: adminBaseURL() });
    try {
      const listed = await admin.get('/__admin/mappings', { headers: adminHeaders() });
      playwrightExpect(listed.ok()).toBeTruthy();
      const body = (await listed.json()) as { mappings: { request?: { headers?: unknown } }[] };
      const withXPath = body.mappings.filter((mapping) => mapping.request?.headers !== undefined);
      playwrightExpect(withXPath).toHaveLength(0);
    } finally {
      await admin.dispose();
    }
  });
});
