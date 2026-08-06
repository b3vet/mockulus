// SPDX-License-Identifier: Apache-2.0
import { UI_PATH, adminBaseURL } from './deployment';
import { expect, seedToken, test, uiUrl } from './harness';

/**
 * The app boots — the first third of U9's list, and the part with the most
 * failure modes that only exist in a browser.
 *
 * A bundle is served under a sub-path, by a Go handler that sends a strict
 * Content-Security-Policy, with a client router that has to understand both.
 * Every case here is about that arrangement rather than about a feature: what
 * is being asserted is that the shipped shape of the thing runs at all.
 */
test.describe('the served application', () => {
  test.beforeEach(async ({ page }) => {
    await seedToken(page);
  });

  test('boots at the prefix the build was based on, with nothing refused', async ({
    page,
    problems,
  }) => {
    await page.goto(uiUrl());

    // The heading proves the module graph executed: the document is static HTML
    // with an empty <div id="app">, so anything on screen came from the script.
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

    // Read from the server rather than asserted as a literal, so that this is a
    // statement about the two agreeing and not a second copy of the constant.
    const base = await page.evaluate(() => document.baseURI);
    expect(new URL(base).pathname).toBe(UI_PATH);

    expect(problems.cspViolations).toEqual([]);
    expect(problems.failedAssets).toEqual([]);
    expect(problems.pageErrors).toEqual([]);
    expect(problems.consoleErrors).toEqual([]);
  });

  test('has its stylesheet applied, which the policy has to allow', async ({ page, problems }) => {
    await page.goto(uiUrl());
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

    // A blocked or missing stylesheet leaves a readable page that is simply
    // unstyled, so nothing else in this suite would notice. The banner's
    // background is a Tailwind utility and nothing else sets it, which makes an
    // opaque colour here evidence that the emitted CSS was fetched and applied.
    const background = await page
      .locator('header')
      .evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(background).not.toBe('rgba(0, 0, 0, 0)');
    expect(background).not.toBe('transparent');

    expect(problems.cspViolations).toEqual([]);
  });

  test('mounts the editor, whose styles are injected at runtime', async ({ page, problems }) => {
    await page.goto(uiUrl('stubs/new'));

    // CodeMirror builds its theme in JavaScript and puts it into the document as
    // a <style> element while the component mounts. That is the one thing this
    // UI does that a `style-src` without 'unsafe-inline' would refuse, and it
    // would refuse it silently: the editor still takes text, so only the look
    // breaks, and no unit test in jsdom can see a look. Asserting that the
    // editable region works *and* that nothing was refused covers both halves.
    const editor = page.getByRole('textbox', { name: 'Stub mapping JSON' });
    await expect(editor).toBeVisible();
    await expect(editor).toContainText('request');

    const gutterBackground = await page
      .locator('.cm-gutters')
      .first()
      .evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(gutterBackground).not.toBe('rgba(0, 0, 0, 0)');

    expect(problems.cspViolations).toEqual([]);
    expect(problems.pageErrors).toEqual([]);
  });

  test('serves a deep link through the SPA fallback and routes it', async ({ page, problems }) => {
    // A path the server has never heard of. It answers with index.html, the
    // bundle boots, and the router has to subtract the base before it can match
    // — which is the arithmetic that breaks when a build is mounted somewhere
    // other than where its `base` said.
    await page.goto(uiUrl('scenarios'));

    await expect(page.getByRole('heading', { level: 1, name: 'Scenarios' })).toBeVisible();
    expect(problems.failedAssets).toEqual([]);
    expect(problems.cspViolations).toEqual([]);
  });

  test('keeps the prefix on in-app navigation, and on reload', async ({ page }) => {
    await page.goto(uiUrl());

    await page
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('link', { name: 'Ops' })
      .click();

    await expect(page.getByRole('heading', { level: 1, name: 'Ops' })).toBeVisible();
    expect(new URL(page.url()).pathname).toBe(`${UI_PATH}ops`);

    // The reload is the point: a history entry the router wrote has to be a URL
    // the server will serve back. A base-path mistake usually survives the click
    // and fails here.
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Ops' })).toBeVisible();
  });

  test("redirects the admin port's root to the UI", async ({ page }) => {
    await page.goto(`${adminBaseURL()}/`);

    expect(new URL(page.url()).pathname).toBe(UI_PATH);
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  });
});
