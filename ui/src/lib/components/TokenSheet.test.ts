// SPDX-License-Identifier: Apache-2.0
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import TokenSheet from './TokenSheet.svelte';
import { createApi, type Api } from '../api.svelte';
import { fakeClient } from '../testing';

function apiUnderTest(): Api {
  return createApi({ baseUrl: 'http://mock.example', createClient: () => fakeClient({}) });
}

describe('TokenSheet', () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  it('stays shut on a deployment that never answers 401', () => {
    render(TokenSheet, { api: apiUnderTest() });

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('opens as a modal dialog with an accessible name once a token is asked for', async () => {
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();

    const dialog = await screen.findByRole('dialog', { name: 'Admin token required' });
    expect(dialog).toHaveAttribute('aria-modal', 'true');
  });

  it('puts focus in the field, so a token can be pasted without pressing Tab', async () => {
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');

    await waitFor(() => expect(screen.getByLabelText('Token')).toHaveFocus());
  });

  it('masks the field, because the value is a credential', async () => {
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');

    expect(screen.getByLabelText('Token')).toHaveAttribute('type', 'password');
  });

  it('refuses to submit an empty token', async () => {
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');

    expect(screen.getByRole('button', { name: 'Use token' })).toBeDisabled();
  });

  it('hands a typed token to the api and closes', async () => {
    const user = userEvent.setup();
    const api = apiUnderTest();
    const retry = vi.fn();
    render(TokenSheet, { api });

    api.requestToken(retry);
    await screen.findByRole('dialog');

    await user.type(screen.getByLabelText('Token'), 's3cret');
    await user.click(screen.getByRole('button', { name: 'Use token' }));

    expect(api.hasToken).toBe(true);
    expect(sessionStorage.getItem('mockulus.admin-token')).toBe('s3cret');
    expect(localStorage.length).toBe(0);
    expect(retry).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('submits on Enter, which is what a paste-and-go actually does', async () => {
    const user = userEvent.setup();
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');

    await user.type(screen.getByLabelText('Token'), 's3cret{Enter}');

    expect(api.hasToken).toBe(true);
  });

  it('closes on Escape without taking a token', async () => {
    const user = userEvent.setup();
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');

    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(api.tokenRequested).toBe(false);
    expect(api.hasToken).toBe(false);
  });

  it('gives focus back to where it came from when it closes', async () => {
    const user = userEvent.setup();
    const api = apiUnderTest();
    // Something outside the dialog holding focus when the 401 lands, which is
    // the ordinary case: the user was on the page doing something.
    const elsewhere = document.createElement('button');
    elsewhere.textContent = 'elsewhere';
    document.body.append(elsewhere);
    render(TokenSheet, { api });
    elsewhere.focus();

    api.requestToken();
    await screen.findByRole('dialog');
    await waitFor(() => expect(screen.getByLabelText('Token')).toHaveFocus());

    await user.keyboard('{Escape}');

    await waitFor(() => expect(elsewhere).toHaveFocus());
    elsewhere.remove();
  });

  it('closes on Cancel without taking a token', async () => {
    const user = userEvent.setup();
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');

    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(api.hasToken).toBe(false);
  });

  it('keeps no copy of a dismissed token in the field', async () => {
    const user = userEvent.setup();
    const api = apiUnderTest();
    render(TokenSheet, { api });

    api.requestToken();
    await screen.findByRole('dialog');
    await user.type(screen.getByLabelText('Token'), 'typed-then-abandoned');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    api.requestToken();

    await waitFor(() => expect(screen.getByLabelText('Token')).toHaveValue(''));
  });
});
