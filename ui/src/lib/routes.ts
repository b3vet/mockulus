// SPDX-License-Identifier: Apache-2.0
import type { Component } from 'svelte';
import About from '../views/About.svelte';
import Journal from '../views/Journal.svelte';
import NearMisses from '../views/NearMisses.svelte';
import Ops from '../views/Ops.svelte';
import Overview from '../views/Overview.svelte';
import Scenarios from '../views/Scenarios.svelte';
import StubDetail from '../views/StubDetail.svelte';
import StubEditor from '../views/StubEditor.svelte';
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
 * Every feature area has its route, its nav entry and its title here, including
 * the ones whose views are still placeholders. The table is deliberately ahead
 * of the views: two stages building different areas at once would otherwise both
 * be editing this file and the nav beside it, which is the one place they would
 * collide. Filling in a placeholder is then a change to one view and nothing
 * else.
 *
 * The three editor paths name one component, which decides between them from the
 * path (`lib/stub-draft.ts`). They are separate routes rather than one route
 * with a mode in component state because all three are worth linking to: "the
 * editor, on this stub" is something to bookmark and to paste to a colleague,
 * and the server's SPA fallback makes such a link survive a reload.
 *
 * `/stubs/new` sits above `/stubs/:id` for readability only — `matchRoute`
 * prefers a static segment over a parametric one whatever the order, so the
 * detail view can never be handed an id of "new".
 */
export const routes: readonly RouteDefinition[] = [
  { path: '/', label: 'Overview', title: 'Overview', component: Overview },
  { path: '/stubs', label: 'Stubs', title: 'Stubs', component: Stubs },
  { path: '/stubs/new', title: 'New stub', component: StubEditor },
  { path: '/stubs/:id', title: 'Stub', component: StubDetail },
  { path: '/stubs/:id/edit', title: 'Edit stub', component: StubEditor },
  { path: '/stubs/:id/duplicate', title: 'Duplicate stub', component: StubEditor },
  { path: '/journal', label: 'Journal', title: 'Journal', component: Journal },
  { path: '/near-misses', label: 'Near misses', title: 'Near misses', component: NearMisses },
  { path: '/scenarios', label: 'Scenarios', title: 'Scenarios', component: Scenarios },
  { path: '/ops', label: 'Ops', title: 'Ops', component: Ops },
  { path: '/about', label: 'About', title: 'About', component: About },
];

/** The routes the primary nav lists, which is the ones that gave themselves a label. */
export const navRoutes: readonly RouteDefinition[] = routes.filter(
  (route) => route.label !== undefined,
);
