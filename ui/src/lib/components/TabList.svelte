<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts" generics="Id extends string">
  import type { Snippet } from 'svelte';
  import { tablistTargetIndex, type TabDefinition } from '../tablist';

  /**
   * A tab list, for the two surfaces that have one.
   *
   * The journal's three outcomes and the near-miss debugger's two modes were
   * hand-rolled separately, sharing only the arrow-key arithmetic, because they
   * were built by two stages that could not edit one file between them. What was
   * duplicated was not the markup so much as the four decisions underneath it —
   * roving `tabindex`, activation on focus, where focus lands after an arrow
   * key, and the `aria-controls`/`aria-labelledby` pairing — and four decisions
   * copied twice is four chances for the two to drift into disagreeing about
   * what a tab is.
   *
   * **Roving tabindex.** Exactly one tab is in the page's tab order at a time,
   * which is what makes the whole list one stop: Tab enters at the selected tab
   * and the next Tab leaves for the panel, rather than walking every tab on the
   * way past.
   *
   * **Activation follows focus.** An arrow key selects as it moves rather than
   * merely focusing. The authoring practices allow either, and this is the right
   * half for both callers: the journal's tabs filter a list already in the
   * browser, and opening the near-miss journal mode costs the read that is the
   * mode's entire purpose. Nothing here is expensive enough to be worth making
   * the reader press Enter as well.
   *
   * **Focus moves through element references** rather than by walking the DOM
   * from the pressed button. The two hand-rolled versions both reached for
   * `currentTarget.parentElement.children[index]`, which is correct only for as
   * long as the tabs stay the immediate children of the tablist element — so a
   * wrapper added for layout would break arrow-key navigation silently, in a
   * place nothing points at.
   */
  interface Props {
    /**
     * What the list is a list of. A tab list has no visible heading, so without
     * this a reader arriving on it is told only that there are tabs.
     */
    label: string;
    tabs: readonly TabDefinition<Id>[];
    selected: Id;
    onselect: (id: Id) => void;
    /**
     * The id given to a tab, so the panel it controls can name it back. Supplied
     * by the caller rather than generated, because the panel is the caller's and
     * both halves of the pairing have to agree.
     */
    tabId: (id: Id) => string;
    /**
     * The id of the panel a tab controls. One panel shared by every tab and one
     * panel per tab are both legitimate, and the two callers do one each.
     */
    panelId: (id: Id) => string;
    /** Rendered after the label. The journal puts its per-outcome count here. */
    trailing?: Snippet<[TabDefinition<Id>]>;
    /** Classes for the tablist element, so it can be spaced to the page it sits on. */
    class?: string;
  }

  let {
    label,
    tabs,
    selected,
    onselect,
    tabId,
    panelId,
    trailing,
    class: className = '',
  }: Props = $props();

  /**
   * The rendered tabs, in list order, so an arrow key can focus the one it
   * selected. Sparse until the list has mounted, which is why every read of it
   * is optional.
   */
  let buttons = $state<(HTMLButtonElement | null)[]>([]);

  function onKeydown(event: KeyboardEvent, index: number) {
    const target = tablistTargetIndex(event.key, index, tabs.length);
    if (target === undefined) {
      return;
    }
    const definition = tabs[target];
    if (definition === undefined) {
      return;
    }
    event.preventDefault();
    onselect(definition.id);
    buttons[target]?.focus();
  }
</script>

<div role="tablist" aria-label={label} class="flex gap-1 {className}">
  {#each tabs as definition, index (definition.id)}
    {@const isSelected = selected === definition.id}
    <button
      bind:this={buttons[index]}
      type="button"
      role="tab"
      id={tabId(definition.id)}
      aria-selected={isSelected}
      aria-controls={panelId(definition.id)}
      tabindex={isSelected ? 0 : -1}
      onclick={() => onselect(definition.id)}
      onkeydown={(event) => onKeydown(event, index)}
      class="rounded-t-md border-b-2 px-3 py-2 text-sm font-medium {isSelected
        ? 'border-sky-600 text-sky-700 dark:text-sky-400'
        : 'border-transparent text-slate-600 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100'}"
    >
      {definition.label}
      {#if trailing}{@render trailing(definition)}{/if}
    </button>
  {/each}
</div>
