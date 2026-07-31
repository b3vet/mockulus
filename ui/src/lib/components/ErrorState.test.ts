// SPDX-License-Identifier: Apache-2.0
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import ErrorState from './ErrorState.svelte';
import { adminError } from '../testing';

describe('ErrorState', () => {
  it('reads code 1010 as configuration rather than failure, and says how to change it', () => {
    render(ErrorState, {
      error: adminError(500, [
        {
          code: 1010,
          title: 'Journal disabled',
          detail: 'the request journal is disabled; set journal_enabled to record and verify',
        },
      ]),
    });

    expect(screen.getByRole('heading', { name: 'The request journal is off' })).toBeInTheDocument();
    expect(screen.getByText(/Nothing is broken here/)).toBeInTheDocument();
    expect(screen.getByText('journal_enabled: true')).toBeInTheDocument();
    expect(screen.getByText('MOCKULUS_JOURNAL_ENABLED=true')).toBeInTheDocument();
  });

  it('explains code 1020 as degraded — what still works, and what to check', () => {
    render(ErrorState, {
      error: adminError(503, [{ code: 1020, title: 'Store unavailable' }]),
    });

    expect(
      screen.getByRole('heading', { name: 'The stub store is unavailable' }),
    ).toBeInTheDocument();
    expect(screen.getByText(/still serving mock traffic/)).toBeInTheDocument();
    expect(screen.getByText('GET /__admin/health')).toBeInTheDocument();
  });

  it('renders every problem a 422 reported, with its JSON pointer', () => {
    render(ErrorState, {
      error: adminError(422, [
        {
          code: 10,
          title: 'Malformed request',
          detail: 'JSONPath does not compile: unclosed [ in "$["',
          source: { pointer: '/matchesJsonPath' },
        },
        {
          code: 1000,
          title: 'Unsupported feature',
          detail: 'not supported in mockulus v1',
          source: { pointer: '/request/multipartPatterns' },
        },
      ]),
    });

    expect(screen.getByRole('heading', { name: 'The server refused the request' })).toBeVisible();
    expect(screen.getByText('/matchesJsonPath')).toBeInTheDocument();
    expect(screen.getByText('/request/multipartPatterns')).toBeInTheDocument();
    expect(screen.getByText(/JSONPath does not compile/)).toBeInTheDocument();
    expect(screen.getByText(/not supported in mockulus v1/)).toBeInTheDocument();
  });

  it('names a 401 for what it is and offers the token sheet', async () => {
    const onauthenticate = vi.fn();
    const user = userEvent.setup();
    render(ErrorState, {
      error: adminError(401, [{ code: 10, title: 'Unauthorized' }]),
      onauthenticate,
    });

    expect(
      screen.getByRole('heading', { name: 'This deployment needs an admin token' }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Enter token' }));
    expect(onauthenticate).toHaveBeenCalledTimes(1);
  });

  it('reads a transport failure as unreachable rather than as a server answer', () => {
    render(ErrorState, { error: new TypeError('Failed to fetch') });

    expect(screen.getByRole('heading', { name: 'Could not reach the admin API' })).toBeVisible();
  });

  it('falls back to the message for anything it does not recognise', () => {
    render(ErrorState, { error: new Error('something unusual') });

    expect(screen.getByRole('heading', { name: 'Something went wrong' })).toBeVisible();
    expect(screen.getByText(/something unusual/)).toBeInTheDocument();
  });

  it('offers a retry only where the caller supplied one', async () => {
    const onretry = vi.fn();
    const user = userEvent.setup();
    const { unmount } = render(ErrorState, { error: new Error('boom'), onretry });

    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect(onretry).toHaveBeenCalledTimes(1);

    unmount();
    render(ErrorState, { error: new Error('boom') });
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });

  it('announces itself politely, since it replaces content that was being waited for', () => {
    render(ErrorState, { error: new Error('boom') });

    expect(screen.getByRole('status')).toBeInTheDocument();
  });
});
