import { describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiClientError, can, describeError } from './client';
import type { ApiEnvelopeResponse } from './types';

function jsonResponse<T>(status: number, body: ApiEnvelopeResponse<T>): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function makeClient(fetchImpl: typeof fetch, tenantId = 'tenant-a', now = () => 1000) {
  let counter = 0;
  return new ApiClient({ tenantId, fetchImpl, now, newId: () => `id-${counter++}` });
}

describe('ApiClient', () => {
  it('binds every envelope to the active tenant and principal scope', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(200, { version: 'v1', data: { ok: true } }));
    const client = makeClient(fetchImpl as unknown as typeof fetch);
    await client.request('/api/v1/project', { principal: { type: 'user', id: 'user-1' }, scope: { user_id: 'user-1', type: 'session', session_id: 'session-1' } });
    const [url, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(url).toBe('/api/v1/project');
    const envelope = JSON.parse(String((init as RequestInit).body));
    expect(envelope.scope.tenant_id).toBe('tenant-a');
    expect(envelope.scope.user_id).toBe('user-1');
    expect(envelope.version).toBe('v1');
    expect(envelope.privacy.visibility).toBe('private');
  });

  it('caches responses and clears the cache when the tenant switches', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(200, { version: 'v1', data: { tenant: 'a' } }));
    const client = makeClient(fetchImpl as unknown as typeof fetch);
    await client.cached('project:session-1', () => client.request('/api/v1/project', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } }));
    await client.cached('project:session-1', () => client.request('/api/v1/project', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } }));
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(client.cacheSize()).toBe(1);
    client.setActiveTenant('tenant-b');
    expect(client.cacheSize()).toBe(0);
    await client.cached('project:session-1', () => client.request('/api/v1/project', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } }));
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    const second = JSON.parse(String((fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[1][1].body));
    expect(second.scope.tenant_id).toBe('tenant-b');
  });

  it('expires cache entries after the ttl', async () => {
    let clock = 0;
    const fetchImpl = vi.fn(async () => jsonResponse(200, { version: 'v1', data: {} }));
    const client = makeClient(fetchImpl as unknown as typeof fetch, 'tenant-a', () => clock);
    await client.cached('k', () => client.request('/api/v1/project', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } }));
    clock = 31_000;
    await client.cached('k', () => client.request('/api/v1/project', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } }));
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('surfaces API error codes and retryability', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(409, { version: 'v1', error: { code: 'CONFLICT', message: 'stale version', retryable: false } }));
    const client = makeClient(fetchImpl as unknown as typeof fetch);
    await expect(client.request('/api/v1/feedback', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } })).rejects.toMatchObject({ code: 'CONFLICT', status: 409, retryable: false });
  });

  it('maps network failures to a retryable error', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error('offline');
    });
    const client = makeClient(fetchImpl as unknown as typeof fetch);
    const error = await client
      .request('/api/v1/project', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } })
      .catch((value: unknown) => value);
    expect(error).toBeInstanceOf(ApiClientError);
    expect(describeError(error)).toEqual({ code: 'NETWORK_ERROR', message: 'offline', retryable: true });
  });

  it('paginates list requests', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(200, { version: 'v1', data: { items: [], total: 0, page: 2, page_size: 50 } }));
    const client = makeClient(fetchImpl as unknown as typeof fetch);
    const page = await client.list('/api/v1/sessions', { principal: { type: 'user', id: 'u' }, scope: { user_id: 'u', type: 'user-global' } }, 2, 50);
    expect(page.page).toBe(2);
    const envelope = JSON.parse(String((fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0][1].body));
    expect(envelope.payload).toEqual({ page: 2, page_size: 50 });
  });

  it('enforces role-based permissions', () => {
    expect(can('owner', 'tenant.manage')).toBe(true);
    expect(can('tenant-admin', 'tenant.manage')).toBe(false);
    expect(can('tenant-admin', 'member.manage')).toBe(true);
    expect(can('member', 'memory.review')).toBe(true);
    expect(can('member', 'agent.manage')).toBe(false);
  });
});
