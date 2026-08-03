// SPDX-License-Identifier: Apache-2.0
import { TOKEN_STORAGE_KEY, UI_PATH, e2eToken } from './deployment';
import { expect, test, uiUrl } from './harness';

/**
 * The token flow — the second of U9's three, and the one with the most to say
 * that no other lane can.
 *
 * The Go corpus pins the load-bearing serving behaviour: assets answer 200
 * without a token while the API answers 401 (SOW decision U4). The unit tests
 * pin where the value is kept and when the sheet opens. Neither of them can see
 * whether an `Authorization` header built by the SDK, in a browser, on a page
 * served from a sub-path, actually arrives at the admin mux — which is the
 * whole of what "the token flow works" means to somebody using this.
 *
 * The suite runs twice, and both configurations are real: a deployment without
 * `admin_auth_token` must never show the sheet, and one with it must show it
 * exactly once and then stop.
 */
const token = e2eToken();

test.describe('a deployment with no admin token', () => {
  test.skip(token !== '', 'this run configured a token');

  test('never asks for one', async ({ page, problems }) => {
    await page.goto(uiUrl());

    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    // The version panel is the first call the app makes. Its answering is what
    // says the API is open, and the sheet's absence is what says the UI noticed.
    await expect(page.getByText('WireMock surface')).toBeVisible();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByText('Admin token set for this tab')).toHaveCount(0);

    expect(
      await page.evaluate((key) => window.sessionStorage.getItem(key), TOKEN_STORAGE_KEY),
    ).toBe(null);
    expect(problems.cspViolations).toEqual([]);
  });
});

test.describe('a deployment behind an admin token', () => {
  test.skip(token === '', 'this run configured no token');

  test('serves the assets, refuses the API, and asks for the token', async ({ page }) => {
    await page.goto(uiUrl());

    // The application itself loaded — a page that could not load would have no
    // sheet to show — and only then was the reader asked. That pairing is the
    // §17 amendment behaving as designed, seen from the browser rather than from
    // a status code.
    await expect(page.getByRole('dialog', { name: 'Admin token required' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  });

  test('carries the token to the admin mux, and keeps it in sessionStorage alone', async ({
    page,
  }) => {
    /** Every admin request the browser made, and what it carried. */
    const adminRequests: { path: string; authorization: string | undefined }[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (url.pathname.startsWith('/__admin/') && !url.pathname.startsWith(UI_PATH)) {
        adminRequests.push({
          path: url.pathname,
          authorization: request.headers()['authorization'],
        });
      }
    });

    await page.goto(uiUrl());
    await page.getByRole('dialog', { name: 'Admin token required' }).waitFor();
    await page.getByLabel('Token', { exact: true }).fill(token);
    await page.getByRole('button', { name: 'Use token' }).click();

    // The refused call is re-run with the token rather than the page reloading,
    // so the answer arriving is the retry queue working.
    await expect(page.getByText('WireMock surface')).toBeVisible();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByText('Admin token set for this tab')).toBeVisible();

    const authorized = adminRequests.filter((request) => request.authorization !== undefined);
    expect(authorized.length).toBeGreaterThan(0);
    for (const request of authorized) {
      expect(request.authorization).toBe(`Token ${token}`);
    }

    // Where it went, and — the part that matters — where it did not. A token in
    // localStorage would outlive the tab and be readable from every other tab on
    // the origin; in a cookie it would be attached by the browser to requests
    // the UI never made, including the asset loads that are exempt by design.
    const kept = await page.evaluate(
      ([key]) => ({
        session: window.sessionStorage.getItem(key),
        local: window.localStorage.getItem(key),
        localCount: window.localStorage.length,
        cookie: document.cookie,
      }),
      [TOKEN_STORAGE_KEY] as const,
    );
    expect(kept.session).toBe(token);
    expect(kept.local).toBe(null);
    expect(kept.localCount).toBe(0);
    expect(kept.cookie).toBe('');

    // And not in the URL, where it would reach history, the `Referer` of
    // anything linked, and every access log in between.
    expect(page.url()).not.toContain(token);
  });

  test('survives a reload without asking again, and forgets on demand', async ({ page }) => {
    await page.goto(uiUrl());
    await page.getByRole('dialog', { name: 'Admin token required' }).waitFor();
    await page.getByLabel('Token', { exact: true }).fill(token);
    await page.getByRole('button', { name: 'Use token' }).click();
    await expect(page.getByText('WireMock surface')).toBeVisible();

    await page.reload();

    await expect(page.getByText('WireMock surface')).toBeVisible();
    await expect(page.getByRole('dialog')).toHaveCount(0);

    await page.getByRole('button', { name: 'Forget it' }).click();

    // Forgetting has to be real rather than cosmetic: the next call 401s and the
    // sheet comes back, which is only true if the stored value went with it.
    expect(
      await page.evaluate((key) => window.sessionStorage.getItem(key), TOKEN_STORAGE_KEY),
    ).toBe(null);
    await page.reload();
    await expect(page.getByRole('dialog', { name: 'Admin token required' })).toBeVisible();
  });
});
