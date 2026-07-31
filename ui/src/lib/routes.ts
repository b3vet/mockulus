// SPDX-License-Identifier: Apache-2.0
import type { Component } from 'svelte';
import About from '../views/About.svelte';
import Overview from '../views/Overview.svelte';
import StubDetail from '../views/StubDetail.svelte';
import Stubs from '../views/Stubs.svelte';

export interface RouteDefinition {
  /** Base-relative path, as produced by `toRoutePath`. A `:name` segment is a parameter. */
  readonly path: string;
  /** Nav label. A route without one is reachable by link but not listed — the detail views. */
  readonly label?: string;
  /** Document title suffix. */
  readonly title: string;
  readonly component: Component;
}

/**
 * The whole route table.
 *
 * What is here is the shell and the stub browser: everything the read side of
 * stubs needs and nothing of the editor. The remaining feature areas the SOW
 * lists — journal, near-miss debugger, scenarios, ops — arrive as later stages
 * fill this in.
 */
export const routes: readonly RouteDefinition[] = [
  { path: '/', label: 'Overview', title: 'Overview', component: Overview },
  { path: '/stubs', label: 'Stubs', title: 'Stubs', component: Stubs },
  { path: '/stubs/:id', title: 'Stub', component: StubDetail },
  { path: '/about', label: 'About', title: 'About', component: About },
];

/** The routes the primary nav lists, which is the ones that gave themselves a label. */
export const navRoutes: readonly RouteDefinition[] = routes.filter(
  (route) => route.label !== undefined,
);
