// SPDX-License-Identifier: Apache-2.0
import { expect as playwrightExpect, request } from '@playwright/test';
import { adminBaseURL, adminHeaders } from './deployment';
import { expect, seedToken, test, uiUrl } from './harness';

/**
 * Checking a mappings file in the browser without importing it.
 *
 * The unit lane already pins what the panel renders for a given report. What it
 * cannot pin is that the button reaches the endpoint, that the endpoint answers
 * the shape the panel was written against, and — the part worth a browser — that
 * pressing it leaves the deployment alone. That last one is asserted from
 * outside the browser, against the admin API, because the panel saying "nothing
 * was written" is exactly the claim under test and cannot be its own evidence.
 *
 * The file carries one mapping that registers and one that cannot, so the batch
 * verdict has something to be about: import is atomic, so the good one would not
 * land either.
 */
const GOOD_PATH = '/e2e/ui-dry-run/keeps';
const BAD_PATH = '/e2e/ui-dry-run/refused';

const BATCH = JSON.stringify({
  mappings: [
    {
      name: 'dry run, valid',
      request: { method: 'GET', urlPath: GOOD_PATH },
      response: { status: 200, body: 'ok' },
    },
    {
      name: 'dry run, refused',
      request: {
        method: 'GET',
        urlPath: BAD_PATH,
        // A real WireMock feature mockulus does not implement, so the refusal is
        // the catalogued 1000 rather than a schema complaint. `customMatcher`
        // is a stated non-goal rather than a roadmap item, so unlike the
        // `equalToXml` this used to name it will not become supported and
        // quietly turn this fixture into a valid document.
        customMatcher: { name: 'com.example.Matcher' },
      },
      response: { status: 200 },
    },
  ],
});

/** How many mappings the deployment holds, asked outside the browser. */
async function mappingCount(): Promise<number> {
  const api = await request.newContext({
    baseURL: adminBaseURL(),
    extraHTTPHeaders: adminHeaders(),
  });
  try {
    const res = await api.get('/__admin/mappings');
    playwrightExpect(res.status()).toBe(200);
    const body = (await res.json()) as { mappings: unknown[] };
    return body.mappings.length;
  } finally {
    await api.dispose();
  }
}

test.describe('checking a file before importing it', () => {
  test.beforeEach(async ({ page }) => {
    await seedToken(page);
  });

  test('reports what would be refused and writes nothing', async ({ page }) => {
    const before = await mappingCount();

    await page.goto(uiUrl('/stubs'));

    // The import panel is behind a disclosure, so the file input does not exist
    // until the button is pressed. Without this the locator waits out the whole
    // timeout against a page that was never going to render it.
    await page.getByRole('button', { name: 'Import…' }).click();

    await page.getByLabel('Choose a file…').setInputFiles({
      name: 'batch.json',
      mimeType: 'application/json',
      buffer: Buffer.from(BATCH),
    });

    await expect(page.getByRole('button', { name: 'Check without writing' })).toBeEnabled();
    await page.getByRole('button', { name: 'Check without writing' }).click();

    // The batch verdict leads, because import is atomic and one refused mapping
    // in two means neither lands.
    await expect(page.getByRole('status')).toContainText('This file would not import');

    // The offending mapping is named, and so is the field.
    await expect(page.getByLabel('Rejected mappings')).toContainText('customMatcher');

    // The claim the panel makes about itself, checked from outside it. The
    // count covers both mappings: the valid one did not land either, which is
    // what atomicity means and what the panel told the reader.
    playwrightExpect(await mappingCount()).toBe(before);
  });

  test('a clean file reports that it would import, and still writes nothing', async ({ page }) => {
    const before = await mappingCount();

    await page.goto(uiUrl('/stubs'));

    const clean = JSON.stringify({
      mappings: [
        {
          name: 'dry run, all valid',
          request: { method: 'GET', urlPath: '/e2e/ui-dry-run/clean' },
          response: { status: 200 },
        },
      ],
    });

    // Same disclosure as above.
    await page.getByRole('button', { name: 'Import…' }).click();

    await page.getByLabel('Choose a file…').setInputFiles({
      name: 'clean.json',
      mimeType: 'application/json',
      buffer: Buffer.from(clean),
    });
    await page.getByRole('button', { name: 'Check without writing' }).click();

    await expect(page.getByRole('status')).toContainText('would register');
    playwrightExpect(await mappingCount()).toBe(before);
  });
});
