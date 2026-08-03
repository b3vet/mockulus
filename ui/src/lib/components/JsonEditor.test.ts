// SPDX-License-Identifier: Apache-2.0
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import JsonEditor, { type JsonEditorController } from './JsonEditor.svelte';

/** Mounts the editor and hands back the controller it published. */
async function mount(value: string) {
  let controller: JsonEditorController | undefined;
  const props = {
    value,
    label: 'Stub mapping JSON',
    describedBy: 'help',
    oncontroller: (next: JsonEditorController | undefined) => {
      controller = next;
    },
  };
  const rendered = render(JsonEditor, props);
  const region = await screen.findByRole('textbox', { name: 'Stub mapping JSON' });
  return { rendered, region, controller: () => controller };
}

describe('JsonEditor', () => {
  it('exposes the document as a named textbox, so it is reachable and identifiable', async () => {
    const { region } = await mount('{\n  "a": 1\n}\n');

    // CodeMirror's editable surface is a contenteditable div; without the
    // content attributes it reaches assistive technology with no name at all.
    expect(region).toHaveAttribute('contenteditable', 'true');
    expect(region).toHaveAttribute('aria-describedby', 'help');
    expect(region.textContent).toContain('"a"');
  });

  it('is in the tab order, and Tab leads back out of it', async () => {
    const user = userEvent.setup();
    const { region } = await mount('{}');

    await user.tab();
    expect(region).toHaveFocus();

    // Tab is deliberately left unbound: an editor that swallowed it would be
    // the one control on the page a keyboard user could not leave.
    await user.tab();
    expect(region).not.toHaveFocus();
  });

  it('publishes a controller while mounted and takes it back on teardown', async () => {
    const { rendered, controller } = await mount('{}');

    expect(controller()).toBeDefined();

    rendered.unmount();
    // A controller left behind would hold a destroyed view, and dispatching
    // into one of those throws rather than doing nothing.
    expect(controller()).toBeUndefined();
  });

  it('moves the selection onto the span it is asked to reveal, and takes focus', async () => {
    const text = '{\n  "method": "GET"\n}\n';
    const { region, controller } = await mount(text);
    const from = text.indexOf('"method"');

    controller()?.reveal(from, from + '"method": "GET"'.length);

    expect(region).toHaveFocus();
    expect(window.getSelection()?.toString()).toContain('"method": "GET"');
  });

  it('clamps a span that runs past the end rather than throwing', async () => {
    const { controller } = await mount('{}');

    // Offsets are resolved against the text as the page last read it, so a
    // keystroke between resolving and revealing can put the end past the
    // document. CodeMirror answers an out-of-range selection with a throw.
    expect(() => controller()?.reveal(0, 9999)).not.toThrow();
  });

  it('takes focus without moving the cursor when asked only to focus', async () => {
    const { region, controller } = await mount('{\n  "a": 1\n}\n');

    controller()?.focus();

    expect(region).toHaveFocus();
    expect(window.getSelection()?.toString()).toBe('');
  });
});
