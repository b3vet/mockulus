// SPDX-License-Identifier: Apache-2.0
import type { Component } from 'svelte';
import About from '../views/About.svelte';
import Overview from '../views/Overview.svelte';

export interface RouteDefinition {
  /** Base-relative path, as produced by `toRoutePath`. */
  readonly path: string;
  /** Nav label. */
  readonly label: string;
  /** Document title suffix. */
  readonly title: string;
  readonly component: Component;
}

/**
 * The whole route table. Two entries is the point: this stage proves the
 * toolchain and the serving contract, and the feature areas (stubs, journal,
 * near-miss, scenarios, ops) arrive as later stages fill it in.
 */
export const routes: readonly RouteDefinition[] = [
  { path: '/', label: 'Overview', title: 'Overview', component: Overview },
  { path: '/about', label: 'About', title: 'About', component: About },
];
