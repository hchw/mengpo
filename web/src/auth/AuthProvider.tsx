import { createContext, useCallback, useContext, useMemo, useReducer, type ReactNode } from 'react';
import type { Tenant } from '../api/types';
import { authReducer, initialAuthState, type AuthState, type AuthUser } from './auth';

export interface AuthSessionResult {
  user: AuthUser;
  tenants: Tenant[];
  activeTenantId: string;
}

export interface RegisterInput {
  email: string;
  password: string;
  displayName?: string;
}

export interface AuthApi {
  register(input: RegisterInput): Promise<AuthSessionResult>;
  login(email: string, password: string): Promise<AuthSessionResult>;
  listTenants(): Promise<Tenant[]>;
  onTenantChanged?(tenantId: string): void;
}

export interface AuthContextValue extends AuthState {
  register(input: RegisterInput): Promise<void>;
  login(email: string, password: string): Promise<void>;
  selectTenant(tenantId: string): Promise<void>;
  logout(): void;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export interface AuthProviderProps {
  api: AuthApi;
  children: ReactNode;
  initialState?: AuthState;
}

export function AuthProvider({ api, children, initialState }: AuthProviderProps) {
  const [state, dispatch] = useReducer(authReducer, initialState ?? initialAuthState);

  const complete = useCallback(
    async (run: () => Promise<AuthSessionResult>) => {
      dispatch({ type: 'login-started' });
      try {
        const result = await run();
        dispatch({ type: 'login-succeeded', user: result.user, tenants: result.tenants, activeTenantId: result.activeTenantId });
        api.onTenantChanged?.(result.activeTenantId);
      } catch (error) {
        dispatch({ type: 'login-failed', error: error instanceof Error ? error.message : 'authentication failed' });
      }
    },
    [api],
  );

  const register = useCallback((input: RegisterInput) => complete(() => api.register(input)), [api, complete]);
  const login = useCallback((email: string, password: string) => complete(() => api.login(email, password)), [api, complete]);

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
    () => ({ ...state, register, login, selectTenant, logout }),
    [state, register, login, selectTenant, logout],
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
