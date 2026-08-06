// SPDX-License-Identifier: Apache-2.0
import { test as base, type Page } from '@playwright/test';
import { TOKEN_STORAGE_KEY, UI_PATH, e2eToken } from './deployment';

/**
 * What the page did that nobody asked it to.
 *
 * This is most of the value of running in a browser at all. The three failures
 * U9 names — the router's base path, the CSP blocking an asset, the token
 * header not travelling — do not announce themselves as assertion failures.
 * They announce themselves as a page that looks nearly right, with the evidence
 * in the console and in the network log. Collecting that evidence and asserting
 * it is empty turns "the heading rendered" into a claim worth something.
 */
export interface PageProblems {
  /**
   * Content Security Policy violations, as the page itself reported them.
   *
   * Collected from the `securitypolicyviolation` event rather than by reading
   * the header, because the header is being tightened in parallel with this
   * suite and a case that asserted its text would either break on a change that
   * improved the policy or, worse, keep passing while meaning something else.
   * The event says the thing that actually matters: something the app needed
   * was refused. What the app needs is the app's business; whether the policy
   * refuses it is the policy's; and the two disagreeing is the defect.
   */
  cspViolations: string[];
  /** Uncaught exceptions. A bundle that half-loads produces these and nothing else. */
  pageErrors: string[];
  /** Requests under the UI prefix that the server did not serve. */
  failedAssets: string[];
  /** Console errors, minus the browser's own note about a deliberate 401. */
  consoleErrors: string[];
}

/**
 * The browser's own line for a response it did not like. It is emitted for
 * every non-2xx, including the 401 the token run is deliberately provoking, so
 * it says nothing this suite has not already asserted more precisely — a case
 * that cares about a status asserts the status.
 */
const HTTP_STATUS_NOISE = /Failed to load resource/i;

export const test = base.extend<{ problems: PageProblems }>({
  /**
   * Instrumentation, installed before the first document loads.
   *
   * The CSP listener has to be an init script: a violation raised while the
   * bundle is being parsed is raised before any test code could attach a
   * listener, and that is precisely the violation worth catching.
   */
  page: async ({ page }, use) => {
    await page.exposeFunction('__mockulusReportCspViolation', (detail: string) => {
      collected(page).cspViolations.push(detail);
    });
    await page.addInitScript(() => {
      document.addEventListener('securitypolicyviolation', (event) => {
        const report = (
          window as unknown as {
            __mockulusReportCspViolation?: (detail: string) => void;
          }
        ).__mockulusReportCspViolation;
        report?.(
          `${event.effectiveDirective} refused ${event.blockedURI || '(inline)'}` +
            (event.sample ? ` — ${event.sample}` : ''),
        );
      });
    });

    page.on('pageerror', (error) => {
      collected(page).pageErrors.push(error.message);
    });
    page.on('console', (message) => {
      if (message.type() === 'error' && !HTTP_STATUS_NOISE.test(message.text())) {
        collected(page).consoleErrors.push(message.text());
      }
    });
    page.on('response', (response) => {
      const url = new URL(response.url());
      if (url.pathname.startsWith(UI_PATH) && response.status() >= 400) {
        collected(page).failedAssets.push(`${response.status()} ${url.pathname}`);
      }
    });

    await use(page);
  },

  problems: async ({ page }, use) => {
    await use(collected(page));
  },
});

export { expect } from '@playwright/test';

/**
 * One problem record per page, kept beside the page rather than in the fixture
 * so that the listeners above — which are attached before the `problems`
 * fixture is ever resolved — write into the same object the test reads.
 */
const records = new WeakMap<Page, PageProblems>();

function collected(page: Page): PageProblems {
  let record = records.get(page);
  if (!record) {
    record = { cspViolations: [], pageErrors: [], failedAssets: [], consoleErrors: [] };
    records.set(page, record);
  }
  return record;
}

/**
 * Puts the run's token where the UI keeps it, before the app boots.
 *
 * This is how a case that is not about the token gets past a token-protected
 * deployment, and it is also an assertion in itself: the UI reads its token
 * from `sessionStorage` under this key and from nowhere else, so a case seeded
 * this way would stop working the moment that changed. On a run with no token
 * configured it does nothing, which is what makes the same case meaningful in
 * both configurations.
 */
export async function seedToken(page: Page): Promise<void> {
  const token = e2eToken();
  if (token === '') {
    return;
  }
  await page.addInitScript(
    ([key, value]) => {
      window.sessionStorage.setItem(key, value);
    },
    [TOKEN_STORAGE_KEY, token] as const,
  );
}

/** A path inside the served application, as the browser would ask for it. */
export function uiUrl(routePath = ''): string {
  return `${UI_PATH}${routePath.replace(/^\//, '')}`;
}
