<!-- SPDX-License-Identifier: Apache-2.0 -->
<script lang="ts" module>
  import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
  import { jsonLanguage } from '@codemirror/lang-json';
  import {
    HighlightStyle,
    bracketMatching,
    indentOnInput,
    syntaxHighlighting,
  } from '@codemirror/language';
  import { EditorSelection, EditorState, type Extension } from '@codemirror/state';
  import {
    EditorView,
    drawSelection,
    highlightActiveLine,
    highlightActiveLineGutter,
    keymap,
    lineNumbers,
  } from '@codemirror/view';
  import { tags } from '@lezer/highlight';

  /**
   * What a page holds on to so it can move the cursor into the document.
   *
   * The 422 rendering is the whole reason this exists: a problem carries a JSON
   * Pointer, `lib/json-pointer.ts` turns that into offsets, and something has to
   * take offsets and make the editor show them. Handing back two methods keeps
   * the pointer arithmetic out of the component and the CodeMirror API out of
   * the page.
   */
  export interface JsonEditorController {
    /** Selects a span, scrolls it into view, and takes focus. */
    reveal(from: number, to: number): void;
    /** Takes focus without moving the cursor. */
    focus(): void;
  }

  /**
   * The six things JSON has, coloured through CSS custom properties rather than
   * literals.
   *
   * `defaultHighlightStyle` is not used, and the reason is the dark mode this UI
   * already follows the operating system into (`app.css`): its palette is chosen
   * against a white page, and on slate-950 the string colour is close to
   * invisible. A style whose colours are variables lets one declaration in
   * `app.css` answer for both schemes, which is where every other colour
   * decision in this tree is made.
   */
  const jsonHighlighting = HighlightStyle.define([
    { tag: tags.propertyName, color: 'var(--mockulus-editor-key)' },
    { tag: tags.string, color: 'var(--mockulus-editor-string)' },
    { tag: tags.number, color: 'var(--mockulus-editor-number)' },
    { tag: [tags.bool, tags.null], color: 'var(--mockulus-editor-atom)' },
    { tag: tags.separator, color: 'var(--mockulus-editor-punctuation)' },
    { tag: tags.brace, color: 'var(--mockulus-editor-punctuation)' },
    { tag: tags.squareBracket, color: 'var(--mockulus-editor-punctuation)' },
    { tag: tags.invalid, color: 'var(--mockulus-editor-invalid)' },
  ]);

  /**
   * The editor's own chrome. Everything here is a variable for the same reason
   * the highlighting is, and the surface deliberately matches the panels around
   * it so the editor reads as part of the page rather than as an embedded
   * application.
   */
  const editorTheme = EditorView.theme({
    '&': {
      backgroundColor: 'var(--mockulus-editor-bg)',
      color: 'var(--mockulus-editor-fg)',
      fontSize: '0.8125rem',
      borderRadius: '0.5rem',
    },
    '&.cm-focused': {
      // The default is the browser's focus ring on a contenteditable, which
      // several browsers draw on the inner element and clip. Drawing it on the
      // editor itself is what makes the keyboard position visible at the moment
      // it matters, which is after a jump to a pointer.
      outline: '2px solid var(--mockulus-editor-focus)',
      outlineOffset: '1px',
    },
    '.cm-content': {
      fontFamily: 'var(--mockulus-editor-font)',
      padding: '0.75rem 0',
      caretColor: 'var(--mockulus-editor-fg)',
    },
    '.cm-gutters': {
      backgroundColor: 'var(--mockulus-editor-gutter-bg)',
      color: 'var(--mockulus-editor-gutter-fg)',
      border: 'none',
      borderTopLeftRadius: '0.5rem',
      borderBottomLeftRadius: '0.5rem',
    },
    '.cm-activeLine': { backgroundColor: 'var(--mockulus-editor-active)' },
    '.cm-activeLineGutter': {
      backgroundColor: 'var(--mockulus-editor-active)',
      color: 'var(--mockulus-editor-fg)',
    },
    '&.cm-focused .cm-cursor': { borderLeftColor: 'var(--mockulus-editor-fg)' },
    '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: 'var(--mockulus-editor-selection)',
    },
    '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': {
      backgroundColor: 'var(--mockulus-editor-bracket)',
      outline: '1px solid var(--mockulus-editor-punctuation)',
    },
    '.cm-nonmatchingBracket': { backgroundColor: 'var(--mockulus-editor-invalid-bg)' },
    '.cm-scroller': { lineHeight: '1.6' },
  });

  /**
   * The extensions the editor runs with.
   *
   * Assembled by hand rather than taken from the `codemirror` meta-package's
   * `basicSetup`, which is a curated bundle of a dozen packages — autocompletion,
   * search, linting, folding — none of which this surface uses. What reaches the
   * browser here reaches the server binary through `go:embed`, so the bundle is
   * the set of features that are actually on screen.
   *
   * **Tab is deliberately not bound to indentation.** CodeMirror leaves it
   * unbound by default so that Tab keeps moving focus, which is the only way out
   * of a text editor for someone navigating by keyboard; binding it would trap
   * them in the one control on the page they cannot escape. Enter still indents
   * the new line, which is what a JSON document actually needs.
   */
  function extensionsFor(label: string, describedBy: string | undefined): Extension[] {
    return [
      lineNumbers(),
      highlightActiveLine(),
      highlightActiveLineGutter(),
      drawSelection(),
      history(),
      indentOnInput(),
      bracketMatching(),
      jsonLanguage,
      syntaxHighlighting(jsonHighlighting),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      EditorView.lineWrapping,
      editorTheme,
      // CodeMirror's editable surface is a `contenteditable` div, which is
      // exposed as a textbox and would otherwise reach a screen reader with no
      // name at all. The description carries the same sentence sighted users
      // read under the editor.
      EditorView.contentAttributes.of({
        'aria-label': label,
        ...(describedBy === undefined ? {} : { 'aria-describedby': describedBy }),
      }),
    ];
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';

  /**
   * A CodeMirror 6 editor over one JSON document (SOW decision U2).
   *
   * The component owns the editor and nothing else: it does not know what the
   * document means, whether it is valid, or what happens when it is saved. What
   * it exposes is the text, two-way, and a controller for moving the cursor.
   */
  interface Props {
    /** The document. Two-way: the editor is the authority while it is mounted. */
    value: string;
    /** The editable region's accessible name. */
    label: string;
    /** Id of the element describing the editor, attached to the editable region. */
    describedBy?: string;
    /**
     * Handed the controller when the editor mounts, and `undefined` when it is
     * torn down.
     *
     * A callback rather than a bound prop, because the direction is one-way: the
     * editor produces the controller and the page consumes it, and a binding
     * would suggest the page could set one. The `undefined` on teardown is not
     * ceremony — a controller left behind would hold a destroyed `EditorView`,
     * and dispatching into one of those throws.
     */
    oncontroller?: (controller: JsonEditorController | undefined) => void;
  }

  let { value = $bindable(), label, describedBy, oncontroller }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let view: EditorView | undefined;

  /**
   * The editor is built once, for the element it is built into, and torn down
   * with it.
   *
   * Everything the constructor reads — the initial document, the label, the
   * callback — is read inside `untrack`, so this effect depends on `host` and on
   * nothing else. Without that it would depend on `value`, which its own update
   * listener writes: every keystroke would destroy the editor and build a new
   * one, taking the cursor, the selection and the undo history with it. It would
   * also depend on `oncontroller`, which a parent passing an inline arrow
   * replaces on every render.
   */
  $effect(() => {
    const parent = host;
    if (!parent) {
      return;
    }
    return untrack(() => mountInto(parent));
  });

  function mountInto(parent: HTMLElement): () => void {
    const editor = new EditorView({
      parent,
      state: EditorState.create({
        doc: value,
        extensions: [
          ...extensionsFor(label, describedBy),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              value = update.state.doc.toString();
            }
          }),
        ],
      }),
    });
    view = editor;
    oncontroller?.({
      reveal(from: number, to: number) {
        const length = editor.state.doc.length;
        // Clamped rather than trusted. Offsets are resolved against the text as
        // the page last read it, and a keystroke between resolving and revealing
        // is enough to put `to` past the end — which CodeMirror answers with a
        // thrown range error rather than a shrug.
        const start = Math.max(0, Math.min(from, length));
        const end = Math.max(start, Math.min(to, length));
        editor.dispatch({
          selection: EditorSelection.single(start, end),
          scrollIntoView: true,
        });
        editor.focus();
      },
      focus() {
        editor.focus();
      },
    });

    return () => {
      oncontroller?.(undefined);
      view = undefined;
      editor.destroy();
    };
  }

  // The other direction: a document replaced from outside — a draft seeded once
  // the stub it edits has loaded — is written into the editor. The guard is what
  // stops this and the update listener above from chasing each other, and it is
  // also what keeps the cursor still while the user is typing, since a
  // round-tripped keystroke would otherwise rebuild the whole document under it.
  $effect(() => {
    const next = value;
    if (!view || view.state.doc.toString() === next) {
      return;
    }
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: next } });
  });
</script>

<div
  bind:this={host}
  class="overflow-hidden rounded-lg border border-slate-300 dark:border-slate-700"
></div>
