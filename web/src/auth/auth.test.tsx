import { describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { AuthProvider, useAuth, type AuthApi } from './AuthProvider';
import { authReducer, initialAuthState, parseSsoCallback } from './auth';
import type { Tenant } from '../api/types';

const tenants: Tenant[] = [
  { id: 'tenant-a', name: 'Acme', status: 'active', role: 'owner' },
  { id: 'tenant-b', name: 'Globex', status: 'active', role: 'member' },
];

function Probe() {
  const auth = useAuth();
  return (
    <div>
      <span data-testid="status">{auth.status}</span>
      <span data-testid="active">{auth.activeTenantId ?? 'none'}</span>
      <span data-testid="tenants">{auth.tenants.map((tenant) => tenant.name).join(',')}</span>
      <span data-testid="error">{auth.error ?? ''}</span>
      <button onClick={() => void auth.handleSsoCallback('/auth/callback?code=abc&state=xyz')}>login</button>
      <button onClick={() => void auth.selectTenant('tenant-b')}>switch</button>
      <button onClick={() => auth.logout()}>logout</button>
    </div>
  );
}

function makeApi(overrides: Partial<AuthApi> = {}): AuthApi {
  return {
    exchangeSso: vi.fn(async () => ({ user: { id: 'user-1', email: 'u@example.com' }, tenants, activeTenantId: 'tenant-a' })),
    listTenants: vi.fn(async () => tenants),
    onTenantChanged: vi.fn(),
    ...overrides,
  };
}

describe('authReducer', () => {
  it('selects the requested active tenant and rejects unknown tenants', () => {
    const authenticated = authReducer(initialAuthState, { type: 'login-succeeded', user: { id: 'u', email: 'e' }, tenants, activeTenantId: 'tenant-b' });
    expect(authenticated.activeTenantId).toBe('tenant-b');
    expect(authReducer(authenticated, { type: 'tenant-selected', tenantId: 'nope' })).toBe(authenticated);
  });

  it('fails login when the user has no active tenant membership', () => {
    const state = authReducer(initialAuthState, { type: 'login-succeeded', user: { id: 'u', email: 'e' }, tenants: [], activeTenantId: '' });
    expect(state.status).toBe('error');
  });
});

describe('parseSsoCallback', () => {
  it('requires code and state', () => {
    expect(parseSsoCallback('https://x/auth/callback?code=1&state=2')).toEqual({ code: '1', state: '2' });
    expect(() => parseSsoCallback('https://x/auth/callback?code=1')).toThrow();
  });
});

describe('AuthProvider', () => {
  it('completes SSO callback, exposes tenants, and clears tenant-scoped cache on switch', async () => {
    const onTenantChanged = vi.fn();
    const api = makeApi({ onTenantChanged });
    render(
      <AuthProvider api={api}>
        <Probe />
      </AuthProvider>,
    );
    act(() => screen.getByText('login').click());
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('authenticated'));
    expect(screen.getByTestId('active').textContent).toBe('tenant-a');
    expect(screen.getByTestId('tenants').textContent).toBe('Acme,Globex');
    expect(onTenantChanged).toHaveBeenLastCalledWith('tenant-a');

    act(() => screen.getByText('switch').click());
    await waitFor(() => expect(screen.getByTestId('active').textContent).toBe('tenant-b'));
    expect(onTenantChanged).toHaveBeenLastCalledWith('tenant-b');
  });

  it('reports login failure without leaking the callback error', async () => {
    const api = makeApi({ exchangeSso: vi.fn(async () => { throw new Error('SSO denied'); }) });
    render(
      <AuthProvider api={api}>
        <Probe />
      </AuthProvider>,
    );
    act(() => screen.getByText('login').click());
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('error'));
    expect(screen.getByTestId('error').textContent).toBe('SSO denied');
  });

  it('logs out to the anonymous state', async () => {
    const api = makeApi();
    render(
      <AuthProvider api={api}>
        <Probe />
      </AuthProvider>,
    );
    act(() => screen.getByText('login').click());
    await waitFor(() => expect(screen.getByTestId('status').textContent).toBe('authenticated'));
    act(() => screen.getByText('logout').click());
    expect(screen.getByTestId('status').textContent).toBe('anonymous');
    expect(screen.getByTestId('active').textContent).toBe('none');
  });
});
