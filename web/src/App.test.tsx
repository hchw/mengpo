import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from './App';
import { ApiClient } from './api/client';
import type { ApiEnvelopeResponse } from './api/types';
import type { AuthApi } from './auth/AuthProvider';

function jsonResponse<T>(status: number, body: ApiEnvelopeResponse<T>): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

describe('App sign-in', () => {
  it('binds console requests to the authenticated user id, not a placeholder', async () => {
    const calls: { url: string; body: any }[] = [];
    const fetchImpl = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(url), body: JSON.parse(String(init?.body)) });
      return jsonResponse(200, { version: 'v1', data: { items: [], total: 0, page: 1, page_size: 20 } });
    }) as unknown as typeof fetch;

    const tenants = [{ id: 'tenant-a', name: 'Demo', status: 'active' as const, role: 'member' as const }];
    const authApi: AuthApi = {
      exchangeSso: vi.fn(async () => ({ user: { id: 'user-42', email: 'dev@mengpo.local' }, tenants, activeTenantId: 'tenant-a' })),
      listTenants: vi.fn(async () => tenants),
    };

    render(<App authApi={authApi} client={new ApiClient({ tenantId: 'unselected', fetchImpl })} />);

    const form = await screen.findByRole('form', { name: 'sign in' });
    await userEvent.click(form.querySelector('button[type="submit"]') as HTMLButtonElement);

    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    const consoleCall = calls.find((call) => call.url.startsWith('/api/v1/') && !call.url.startsWith('/api/v1/auth/'));
    expect(consoleCall).toBeDefined();
    expect(consoleCall?.body.principal).toEqual({ type: 'user', id: 'user-42' });
    expect(consoleCall?.body.scope.user_id).toBe('user-42');
    expect(consoleCall?.body.scope.tenant_id).toBe('tenant-a');
  });
});
