// SPDX-License-Identifier: Apache-2.0
import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { e2eToken } from './deployment';
import { expect, seedToken, test, uiUrl } from './harness';

/**
 * The automated half of the accessibility sweep.
 *
 * It is in the nightly browser lane and not in the unit lane, deliberately.
 * Half of what axe checks cannot be checked without a rendering engine at all:
 * colour contrast needs computed styles against real backgrounds, and jsdom
 * computes neither. Running it here also means it sees the shipped bundle — the
 * emitted Tailwind stylesheet, the CodeMirror theme injected at runtime — rather
 * than a component in isolation with no page around it.
 *
 * It is an automated pass and not the sweep. Roughly a third of the WCAG
 * criteria are machine-checkable; focus order, focus restoration and whether an
 * accessible name says anything useful are read, not computed, and those were
 * gone through by hand. What this catches is the regression: a colour changed to
 * one that no longer clears the floor, a control that lost its label.
 */
const SURFACES: readonly { readonly path: string; readonly heading: string }[] = [
  { path: '', heading: 'Overview' },
  { path: 'stubs', heading: 'Stubs' },
  { path: 'stubs/new', heading: 'New stub' },
  { path: 'journal', heading: 'Journal' },
  { path: 'near-misses', heading: 'Near-miss debugger' },
  { path: 'scenarios', heading: 'Scenarios' },
  { path: 'ops', heading: 'Ops' },
  { path: 'about', heading: 'About' },
];

const SCHEMES = ['light', 'dark'] as const;

/**
 * The criteria the sweep is held to: WCAG 2.1 A and AA, which is the level the
 * project's user documentation claims and the level a colour palette can be
 * chosen against. Best-practice rules are excluded — they are opinions worth
 * reading, not a bar to fail a nightly on.
 */
function audit(page: Page) {
  return new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']);
}

/** What a finding looks like when it is reported, so a failure names the element. */
function describe(results: Awaited<ReturnType<ReturnType<typeof audit>['analyze']>>): string {
  return results.violations
    .map(
      (violation) =>
        `${violation.id} (${violation.impact}): ${violation.help}\n` +
        violation.nodes
          .map((node) => `    ${node.target.join(' ')}\n    ${node.failureSummary}`)
          .join('\n'),
    )
    .join('\n\n');
}

for (const scheme of SCHEMES) {
  test.describe(`${scheme} scheme`, () => {
    test.use({ colorScheme: scheme });

    for (const surface of SURFACES) {
      test(`${surface.heading} has no WCAG A/AA violations`, async ({ page }) => {
        await seedToken(page);
        await page.goto(uiUrl(surface.path));
        await expect(page.getByRole('heading', { level: 1, name: surface.heading })).toBeVisible();

        const results = await audit(page).analyze();
        expect(describe(results)).toBe('');
      });
    }
  });
}

test.describe('the surfaces that are only reachable through an interaction', () => {
  test('the token sheet', async ({ page }) => {
    test.skip(e2eToken() === '', 'no token configured, so the sheet never opens');

    await page.goto(uiUrl());
    await expect(page.getByRole('dialog', { name: 'Admin token required' })).toBeVisible();

    const results = await audit(page).analyze();
    expect(describe(results)).toBe('');
  });

  test('a confirmation dialog over the page it belongs to', async ({ page }) => {
    await seedToken(page);
    await page.goto(uiUrl('scenarios'));
    await expect(page.getByRole('heading', { level: 1, name: 'Scenarios' })).toBeVisible();

    await page.getByRole('button', { name: 'Reset all scenarios' }).click();
    await expect(page.getByRole('dialog')).toBeVisible();

    const results = await audit(page).analyze();
    expect(describe(results)).toBe('');
  });

  test('the danger zone, whose confirmations are the ones that gate on typing', async ({
    page,
  }) => {
    await seedToken(page);
    await page.goto(uiUrl('ops'));
    await expect(page.getByRole('heading', { level: 1, name: 'Ops' })).toBeVisible();

    // Everything on the page has loaded by the time the danger zone's heading is
    // on screen, which is what makes this the widest single audit in the suite:
    // four panels, a file list and a settings form.
    const results = await audit(page).analyze();
    expect(describe(results)).toBe('');
  });
});

test.describe('the keyboard path a reader without a mouse takes', () => {
  test('the skip link is the first stop and it lands somewhere', async ({ page }) => {
    await seedToken(page);
    await page.goto(uiUrl());
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

    await page.keyboard.press('Tab');
    const skip = page.getByRole('link', { name: 'Skip to content' });
    await expect(skip).toBeFocused();
    // Hidden until it is focused, and visible the moment it is — a skip link
    // nobody can see is one nobody uses.
    await expect(skip).toBeVisible();

    await page.keyboard.press('Enter');

    // The real browser assertion, and the reason this is not a unit test: after
    // following the link, the *next* Tab must land inside the main region rather
    // than back at the top of the header.
    await page.keyboard.press('Tab');
    const insideMain = await page.evaluate(() => {
      const active = document.activeElement;
      const main = document.querySelector('main');
      return active !== null && main !== null && main.contains(active);
    });
    expect(insideMain).toBe(true);
  });

  test('a navigation moves focus into the view it opened', async ({ page }) => {
    await seedToken(page);
    await page.goto(uiUrl());
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

    await page
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('link', { name: 'Near misses' })
      .click();
    await expect(page.getByRole('heading', { level: 1, name: 'Near-miss debugger' })).toBeVisible();

    // Without this the reader is left focused on a link in the header — or, when
    // the control that navigated unmounts with its view, on nothing at all, with
    // the next Tab starting again from the top of the document.
    const focusedMain = await page.evaluate(() => document.activeElement?.tagName);
    expect(focusedMain).toBe('MAIN');
  });

  test('the tab list is one stop, and the arrows move inside it', async ({ page }) => {
    await seedToken(page);
    await page.goto(uiUrl('near-misses'));
    await expect(page.getByRole('heading', { level: 1, name: 'Near-miss debugger' })).toBeVisible();

    const compose = page.getByRole('tab', { name: 'Compose a request' });
    const fromJournal = page.getByRole('tab', { name: 'From the journal' });

    await compose.focus();
    await page.keyboard.press('ArrowRight');

    await expect(fromJournal).toBeFocused();
    await expect(fromJournal).toHaveAttribute('aria-selected', 'true');

    // And Tab leaves the list rather than walking the rest of it, which is what
    // "one stop in the tab order" means.
    await page.keyboard.press('Tab');
    await expect(compose).not.toBeFocused();
    await expect(fromJournal).not.toBeFocused();
  });
});
