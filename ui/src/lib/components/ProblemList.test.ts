// SPDX-License-Identifier: Apache-2.0
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import ProblemList from './ProblemList.svelte';
import { resolveJsonPointer, type DocumentSpan } from '../json-pointer';

const DOCUMENT = `{
  "request": {
    "method": "GET",
    "multipartPatterns": []
  }
}`;

function mount(
  problems: { code: number; title?: string; detail?: string; source?: { pointer?: string } }[],
  text = DOCUMENT,
) {
  const reveal = vi.fn<(span: DocumentSpan) => void>();
  render(ProblemList, {
    problems,
    locate: (pointer: string) => resolveJsonPointer(text, pointer),
    reveal,
  });
  return { reveal };
}

const list = () => screen.getByRole('list', { name: 'Problems the server reported' });

describe('ProblemList', () => {
  it('renders every problem the server reported, not only the first', () => {
    mount([
      { code: 1000, title: 'Unsupported feature', detail: 'multipartPatterns is not supported' },
      { code: 1003, title: 'Invalid regular expression', detail: 'pattern does not compile' },
      { code: 10, title: 'Malformed request', detail: 'method must be a string' },
    ]);

    // The collect-all envelope is only worth the round trip it saves if all of
    // it is on screen.
    expect(within(list()).getAllByRole('listitem')).toHaveLength(3);
    expect(screen.getByText(/multipartPatterns is not supported/)).toBeInTheDocument();
    expect(screen.getByText(/pattern does not compile/)).toBeInTheDocument();
    expect(screen.getByText(/method must be a string/)).toBeInTheDocument();
  });

  it('takes the reader to the field a pointer names', async () => {
    const user = userEvent.setup();
    const { reveal } = mount([
      {
        code: 1000,
        title: 'Unsupported feature',
        detail: 'multipartPatterns is not supported in mockulus v1',
        source: { pointer: '/request/multipartPatterns' },
      },
    ]);

    await user.click(screen.getByRole('button', { name: 'Go to /request/multipartPatterns' }));

    const at = DOCUMENT.indexOf('"multipartPatterns"');
    expect(reveal).toHaveBeenCalledWith({
      resolved: true,
      from: at,
      to: at + '"multipartPatterns": []'.length,
    });
    expect(screen.getByRole('status')).toHaveTextContent(
      'Moved the cursor to /request/multipartPatterns.',
    );
  });

  it('says a pointer is not in the document instead of offering a control that would do nothing', () => {
    mount([
      {
        code: 1000,
        title: 'Unsupported feature',
        detail: 'equalToXml is not supported in mockulus v1',
        source: { pointer: '/request/bodyPatterns/0/equalToXml' },
      },
    ]);

    expect(
      screen.queryByRole('button', { name: /Go to \/request\/bodyPatterns/ }),
    ).not.toBeInTheDocument();
    // Naming the deepest part that does resolve separates "you deleted it" from
    // "the server is naming a field you have not written".
    expect(screen.getByText(/deepest part that does resolve is \/request/)).toBeInTheDocument();
  });

  it('handles a problem the server attached to no field at all', () => {
    mount([{ code: 109, title: 'Duplicate stub mapping ID', detail: 'id already exists' }]);

    expect(screen.getByText(/the server named no field/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Go to/ })).not.toBeInTheDocument();
  });

  it('re-resolves on the click, so a document edited since rendering cannot move the cursor wrongly', async () => {
    const user = userEvent.setup();
    // `locate` reads a document that no longer contains the field: the row was
    // rendered against one text and the click happens against another.
    let text = DOCUMENT;
    const reveal = vi.fn<(span: DocumentSpan) => void>();
    render(ProblemList, {
      problems: [
        { code: 1000, detail: 'unsupported', source: { pointer: '/request/multipartPatterns' } },
      ],
      locate: (pointer: string) => resolveJsonPointer(text, pointer),
      reveal,
    });

    const button = screen.getByRole('button', { name: 'Go to /request/multipartPatterns' });
    text = '{ "request": { "method": "GET" } }';
    await user.click(button);

    expect(reveal).not.toHaveBeenCalled();
    expect(screen.getByRole('status')).toHaveTextContent(
      '/request/multipartPatterns is not in this document.',
    );
  });
});
