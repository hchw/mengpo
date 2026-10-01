import { createContext, useCallback, useContext, useMemo, useReducer, type ReactNode } from 'react';
import type { Tenant } from '../api/types';
import { authReducer, initialAuthState, parseSsoCallback, type AuthState, type AuthUser } from './auth';

export interface AuthApi {
  exchangeSso(code: string, state: string): Promise<{ user: AuthUser; tenants: Tenant[]; activeTenantId: string }>;
  listTenants(): Promise<Tenant[]>;
  onTenantChanged?(tenantId: string): void;
}

export interface AuthContextValue extends AuthState {
  loginUrl(returnTo: string): string;
  handleSsoCallback(url: string): Promise<void>;
  selectTenant(tenantId: string): Promise<void>;
  logout(): void;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export interface AuthProviderProps {
  api: AuthApi;
  children: ReactNode;
  loginBaseUrl?: string;
  initialState?: AuthState;
}

export function AuthProvider({ api, children, loginBaseUrl = '/api/v1/auth/sso', initialState }: AuthProviderProps) {
  const [state, dispatch] = useReducer(authReducer, initialState ?? initialAuthState);

  const loginUrl = useCallback(
    (returnTo: string) => {
      const url = new URL(loginBaseUrl, 'https://console.invalid');
      url.searchParams.set('redirect_uri', returnTo);
      return url.pathname + url.search;
    },
    [loginBaseUrl],
  );

  const handleSsoCallback = useCallback(
    async (callbackUrl: string) => {
      dispatch({ type: 'login-started' });
      try {
        const { code, state: ssoState } = parseSsoCallback(callbackUrl);
        const result = await api.exchangeSso(code, ssoState);
        dispatch({ type: 'login-succeeded', user: result.user, tenants: result.tenants, activeTenantId: result.activeTenantId });
        api.onTenantChanged?.(result.activeTenantId);
      } catch (error) {
        dispatch({ type: 'login-failed', error: error instanceof Error ? error.message : 'login failed' });
      }
    },
    [api],
  );

  const selectTenant = useCallback(
    async (tenantId: string) => {
      dispatch({ type: 'tenant-selected', tenantId });
      api.onTenantChanged?.(tenantId);
    },
    [api],
  );

  const logout = useCallback(() => {
    dispatch({ type: 'logout' });
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({ ...state, loginUrl, handleSsoCallback, selectTenant, logout }),
    [state, loginUrl, handleSsoCallback, selectTenant, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
}
