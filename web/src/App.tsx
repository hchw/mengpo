import { useEffect, useMemo } from 'react';
import { BrowserRouter } from 'react-router-dom';
import { ApiClient } from './api/client';
import { ConsoleApi } from './api/console';
import { AuthProvider, useAuth, type AuthApi } from './auth/AuthProvider';
import { ConsoleProvider } from './app/ConsoleContext';
import { AppRoutes } from './app/AppRoutes';
import { LanguageProvider } from './i18n';
import type { Tenant } from './api/types';

export interface AppProps {
  authApi?: AuthApi;
  client?: ApiClient;
  principalId?: string;
}

function ConsoleShell({ client, principalId = 'current-user' }: { client: ApiClient; principalId?: string }) {
  const auth = useAuth();

  // Complete an SSO redirect when the console is opened with ?code=…&state=…
  useEffect(() => {
    if (auth.status !== 'anonymous') {
      return;
    }
    const params = new URLSearchParams(window.location.search);
    if (params.has('code') && params.has('state')) {
      void auth.handleSsoCallback(window.location.href);
    }
  }, [auth]);

  // Bind the tenant before children render: page effects run before a parent
  // effect, so syncing here avoids one tenant-scoped request under the
  // placeholder tenant. setActiveTenant is idempotent.
  if (auth.activeTenantId) {
    client.setActiveTenant(auth.activeTenantId);
  }

  // The trusted server identity is derived from the session, so the console
  // principal and scope must carry the authenticated user id or every
  // tenant-scoped command is rejected as forbidden.
  const consoleApi = useMemo(
    () => new ConsoleApi(client, { type: 'user', id: auth.user?.id ?? principalId }),
    [client, auth.user?.id, principalId],
  );

  return (
    <ConsoleProvider api={consoleApi}>
      <AppRoutes />
    </ConsoleProvider>
  );
}

export function App({ authApi, client, principalId }: AppProps) {
  const resolvedClient = useMemo(() => client ?? new ApiClient({ tenantId: 'unselected' }), [client]);
  const resolvedAuthApi = authApi ?? createDefaultAuthApi();
  return (
    <LanguageProvider>
      <AuthProvider api={resolvedAuthApi}>
        <BrowserRouter>
          <ConsoleShell client={resolvedClient} principalId={principalId} />
        </BrowserRouter>
      </AuthProvider>
    </LanguageProvider>
  );
}

function toTenant(raw: { id: string; name: string; roles?: string[] }): Tenant {
  return {
    id: raw.id,
    name: raw.name,
    status: 'active',
    role: (raw.roles?.[0] as Tenant['role']) ?? 'member',
  };
}

// createDefaultAuthApi talks to the /api/v1/auth/* endpoints. Responses are v1
// envelopes, so it unwraps `data`; after exchange it binds the session to a
// tenant so the console can issue tenant-scoped commands.
function createDefaultAuthApi(): AuthApi {
  const request = async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const response = await fetch(path, {
      credentials: 'same-origin',
      ...init,
      headers: { 'content-type': 'application/json', ...init?.headers },
    });
    if (!response.ok) {
      throw new Error(`auth request failed: ${response.status}`);
    }
    const body = await response.json();
    return (body?.data ?? body) as T;
  };
  return {
    exchangeSso: async (assertion) => {
      const data = await request<{ user: { id: string; email: string }; tenants: { id: string; name: string; roles?: string[] }[] }>(
        '/api/v1/auth/sso/exchange',
        { method: 'POST', body: JSON.stringify({ assertion }) },
      );
      const tenants = (data.tenants ?? []).map(toTenant);
      const activeTenantId = tenants[0]?.id ?? '';
      if (activeTenantId) {
        await request('/api/v1/auth/tenant-context', {
          method: 'POST',
          body: JSON.stringify({ tenant_id: activeTenantId }),
        });
      }
      return { user: data.user, tenants, activeTenantId };
    },
    listTenants: async () =>
      (await request<{ tenants: { id: string; name: string; roles?: string[] }[] }>('/api/v1/auth/tenants')).tenants.map(toTenant),
  };
}
