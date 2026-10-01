import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { AuthProvider, type AuthApi } from '../auth/AuthProvider';
import { ConsoleProvider } from './ConsoleContext';
import { AppRoutes } from './AppRoutes';
import { fakeConsoleApi } from '../test/fakes';
import type { AuthState } from '../auth/auth';

const authenticated: AuthState = {
  status: 'authenticated',
  user: { id: 'user-1', email: 'u@example.com' },
  tenants: [{ id: 'tenant-a', name: 'Acme', status: 'active', role: 'tenant-admin' }],
  activeTenantId: 'tenant-a',
};

const authApi: AuthApi = {
  exchangeSso: async () => ({ user: authenticated.user!, tenants: authenticated.tenants, activeTenantId: 'tenant-a' }),
  listTenants: async () => authenticated.tenants,
};

const user = userEvent.setup();

function renderRoutes(initialPath: string) {
  return render(
    <AuthProvider api={authApi} initialState={authenticated}>
      <ConsoleProvider api={fakeConsoleApi()}>
        <MemoryRouter initialEntries={[initialPath]}>
          <AppRoutes />
        </MemoryRouter>
      </ConsoleProvider>
    </AuthProvider>,
  );
}

describe('AppRoutes', () => {
  it('renders the dashboard at the root route', async () => {
    renderRoutes('/');
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Dashboard' })).toBeInTheDocument());
  });

  it('navigates between console routes', async () => {
    renderRoutes('/');
    await user.click(screen.getByRole('link', { name: 'Projection Debugger' }));
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Projection Debugger' })).toBeInTheDocument());
    await user.click(screen.getByRole('link', { name: 'Members' }));
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Members & Agents' })).toBeInTheDocument());
  });

  it('redirects unknown routes to the dashboard', async () => {
    renderRoutes('/does-not-exist');
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Dashboard' })).toBeInTheDocument());
  });

  it('requires authentication', () => {
    render(
      <AuthProvider api={authApi}>
        <ConsoleProvider api={fakeConsoleApi()}>
          <MemoryRouter initialEntries={['/']}>
            <AppRoutes />
          </MemoryRouter>
        </ConsoleProvider>
      </AuthProvider>,
    );
    expect(screen.getByText('Sign in required')).toBeInTheDocument();
  });
});