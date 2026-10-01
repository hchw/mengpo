import type { Tenant } from '../api/types';

export interface AuthUser {
  id: string;
  email: string;
}

export type AuthStatus = 'anonymous' | 'authenticating' | 'authenticated' | 'error';

export interface AuthState {
  status: AuthStatus;
  user?: AuthUser;
  tenants: Tenant[];
  activeTenantId?: string;
  error?: string;
}

export const initialAuthState: AuthState = {
  status: 'anonymous',
  tenants: [],
};

export type AuthAction =
  | { type: 'login-started' }
  | { type: 'login-succeeded'; user: AuthUser; tenants: Tenant[]; activeTenantId: string }
  | { type: 'login-failed'; error: string }
  | { type: 'tenants-loaded'; tenants: Tenant[] }
  | { type: 'tenant-selected'; tenantId: string }
  | { type: 'logout' };

export function authReducer(state: AuthState, action: AuthAction): AuthState {
  switch (action.type) {
    case 'login-started':
      return { ...state, status: 'authenticating', error: undefined };
    case 'login-succeeded': {
      if (action.tenants.length === 0) {
        return { ...state, status: 'error', error: 'no active tenant membership' };
      }
      const active = action.tenants.find((tenant) => tenant.id === action.activeTenantId) ?? action.tenants[0];
      return { status: 'authenticated', user: action.user, tenants: action.tenants, activeTenantId: active.id, error: undefined };
    }
    case 'login-failed':
      return { ...state, status: 'error', error: action.error };
    case 'tenants-loaded':
      return { ...state, tenants: action.tenants, activeTenantId: state.activeTenantId && action.tenants.some((tenant) => tenant.id === state.activeTenantId) ? state.activeTenantId : action.tenants[0]?.id };
    case 'tenant-selected':
      if (!state.tenants.some((tenant) => tenant.id === action.tenantId)) {
        return state;
      }
      return { ...state, activeTenantId: action.tenantId };
    case 'logout':
      return { ...initialAuthState };
    default:
      return state;
  }
}

export function activeTenant(state: AuthState): Tenant | undefined {
  return state.tenants.find((tenant) => tenant.id === state.activeTenantId);
}

// parseSsoCallback extracts the authorization code and state from the SSO
// redirect. Missing state is rejected to prevent CSRF-style login injection.
export function parseSsoCallback(url: string): { code: string; state: string } {
  const parsed = new URL(url, 'https://console.invalid');
  const code = parsed.searchParams.get('code') ?? '';
  const state = parsed.searchParams.get('state') ?? '';
  if (!code || !state) {
    throw new Error('SSO callback is missing code or state');
  }
  return { code, state };
}
