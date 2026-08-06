// SPDX-License-Identifier: Apache-2.0
import { defineConfig, devices } from '@playwright/test';
import { adminBaseURL, e2eEnv, mockBaseURL } from './e2e/deployment';

/**
 * The browser smoke of SOW decision U9.
 *
 * **Nightly, and non-blocking.** The PR lane is held to zero flake, and a
 * browser suite is the likeliest thing in this repository to break that: it
 * owns a real process, a real port and a real renderer, and every one of those
 * can be slow on a shared runner for reasons that have nothing to do with the
 * change under test. The serving contract — that the prefix answers, that the
 * SPA falls back, that assets are exempt from the admin token while the API is
 * not — is gated on every PR by the Go corpus, which needs no browser to say
 * so. What is left for a browser is the class of failure the corpus cannot see
 * and vitest cannot either, and that class is worth a nightly run rather than a
 * gate: it is found the morning after rather than in review, and U9 promotes
 * this to blocking only on evidence that it has earned it.
 *
 * **Against the embedded bundle, never the dev server.** The three failures
 * this exists to catch are all failures of the *shipped* arrangement. The
 * router's base path is `/__admin/mockulus/ui/` in a real build and `/` under
 * vite; the CSP is a header the Go handler sends and the dev server does not;
 * and the token header only travels once the SDK is talking to the admin mux
 * rather than to a proxy. A suite against `vite dev` would pass through all
 * three and report nothing.
 */
export default defineConfig({
  testDir: './e2e',
  testMatch: /.*\.spec\.ts/,
  // Serial, deliberately. Every case in this suite writes to one deployment's
  // one snapshot — a stub registered by one test is visible to every other —
  // and a browser suite that has to reason about which of four workers created
  // a mapping is a browser suite that flakes. It is small enough that the time
  // saved would not pay for the reasoning.
  workers: 1,
  fullyParallel: false,
  // Nothing here is allowed to be flaky-but-passing. A retry would hide exactly
  // the intermittent CSP or timing problem the suite exists to surface, and the
  // lane is non-blocking, so a failure costs an issue rather than a build.
  retries: 0,
  // A `test.only` left in the source narrows the nightly to one case while
  // still reporting green, which is the failure mode of a non-blocking lane
  // nobody is watching closely. It fails the run in CI and is left alone
  // locally, where narrowing is the point of typing it.
  forbidOnly: !!process.env.CI,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['github'], ['list']] : [['list']],
  globalSetup: './e2e/global-setup.ts',

  use: {
    baseURL: adminBaseURL(),
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },

  // Chromium alone. The suite asks three questions — does the bundle boot under
  // the served CSP, does the router understand its base path, does the token
  // header travel — and none of them has an answer that differs by engine. A
  // second browser would double the runtime and the download for coverage the
  // questions do not have.
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],

  /**
   * The deployment under test: the binary `make build` produced, with the
   * bundle `make ui-build` embedded in it.
   *
   * Playwright owns the process so that it is torn down even when a case
   * throws. Readiness is `/readyz` rather than `/__admin/version` because the
   * token run protects the admin API and this probe carries no credential —
   * `/readyz` is outside the guarded mux and answers either way. That makes it
   * a liveness check and nothing more, which is why `global-setup.ts` asks the
   * server what it is before the first case runs.
   */
  webServer: {
    command: '../bin/mockulus',
    url: `${adminBaseURL()}/readyz`,
    // Never reuse. A server already on this port is something this suite did
    // not start and cannot vouch for, and a probe that adopts a stranger is how
    // a run reports one deployment's answers as another's.
    reuseExistingServer: false,
    timeout: 30_000,
    stdout: 'pipe',
    stderr: 'pipe',
    env: e2eEnv(),
  },

  metadata: {
    adminBaseURL: adminBaseURL(),
    mockBaseURL: mockBaseURL(),
  },
});
