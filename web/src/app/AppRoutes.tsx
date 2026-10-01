import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { SignInPanel } from '../auth/SignInPanel';
import { useConsole } from './ConsoleContext';
import { BackgroundMemoryPage } from '../pages/BackgroundMemoryPage';
import { CandidateReviewPage } from '../pages/CandidateReviewPage';
import { DashboardPage } from '../pages/DashboardPage';
import { FailureAnalysisPage } from '../pages/FailureAnalysisPage';
import { MembersPage } from '../pages/MembersPage';
import { MemoryEvaluationPage } from '../pages/MemoryEvaluationPage';
import { ProjectionDebuggerPage } from '../pages/ProjectionDebuggerPage';
import { SessionExplorerPage } from '../pages/SessionExplorerPage';
import { SettingsPage } from '../pages/SettingsPage';
import { StateView } from '../components/states';
import { ErrorBoundary } from '../components/ErrorBoundary';
import { NavIcon } from '../components/icons';
import type { ConsoleApi } from '../api/console';

const NAV = [
  { to: '/', label: 'Dashboard', icon: 'dashboard' },
  { to: '/sessions', label: 'Sessions', icon: 'sessions' },
  { to: '/memories', label: 'Background Memory', icon: 'memories' },
  { to: '/candidates', label: 'Candidate Review', icon: 'candidates' },
  { to: '/debugger', label: 'Projection Debugger', icon: 'debugger' },
  { to: '/failures', label: 'Failure Analysis', icon: 'failures' },
  { to: '/evaluation', label: 'Evaluation', icon: 'evaluation' },
  { to: '/members', label: 'Members', icon: 'members' },
  { to: '/settings', label: 'Settings', icon: 'settings' },
];

export interface AppRoutesProps {
  api?: ConsoleApi;
  authenticatedOverride?: boolean;
}

export function AppRoutes({ api, authenticatedOverride }: AppRoutesProps) {
  const auth = useAuth();
  const location = useLocation();
  const contextApi = useConsoleOrUndefined();
  const consoleApi = api ?? contextApi;
  const authenticated = authenticatedOverride ?? auth.status === 'authenticated';

  if (!authenticated) {
    return <LoginPage />;
  }
  if (!consoleApi) {
    return <StateView kind="error" description="Console API is not configured." />;
  }

  const activeTenant = auth.tenants.find((tenant) => tenant.id === auth.activeTenantId);
  const pageTitle = NAV.find((item) => (item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to)))?.label ?? '';
  const initials = (auth.user?.email ?? '?').slice(0, 1).toUpperCase();

  return (
    <div className="console">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            孟
          </span>
          <span className="brand-text">
            <strong>Mengpo</strong>
            <small>Memory Console</small>
          </span>
        </div>

        <nav aria-label="primary" className="nav">
          {NAV.map((item) => (
            <NavLink key={item.to} to={item.to} end={item.to === '/'} className={({ isActive }) => `nav-link${isActive ? ' active' : ''}`}>
              <NavIcon name={item.icon} />
              <span className="nav-label">{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <div className="sidebar-foot">
          <div className="user-chip">
            <span className="avatar" aria-hidden="true">
              {initials}
            </span>
            <span className="user-meta">
              <strong>{auth.user?.email ?? 'signed in'}</strong>
              <small>{activeTenant?.role ?? 'member'}</small>
            </span>
          </div>
          <button type="button" className="btn-ghost" onClick={() => auth.logout()}>
            <NavIcon name="logout" />
            <span>Sign out</span>
          </button>
        </div>
      </aside>

      <div className="content">
        <header className="topbar">
          <div className="topbar-title">{pageTitle}</div>
          <div className="topbar-actions">
            <label className="select">
              <span className="sr-only">Active tenant</span>
              <select value={auth.activeTenantId ?? ''} onChange={(event) => void auth.selectTenant(event.target.value)}>
                {auth.tenants.map((tenant) => (
                  <option key={tenant.id} value={tenant.id}>
                    {tenant.name}
                  </option>
                ))}
              </select>
              <NavIcon name="chevron" />
            </label>
            <span className="env-pill">
              <NavIcon name="pulse" width={13} height={13} />
              live
            </span>
          </div>
        </header>

        <main>
          <ErrorBoundary>
            <Routes>
              <Route path="/" element={<DashboardPage api={consoleApi} />} />
              <Route path="/sessions" element={<SessionExplorerPage api={consoleApi} />} />
              <Route path="/memories" element={<BackgroundMemoryPage api={consoleApi} />} />
              <Route path="/candidates" element={<CandidateReviewPage api={consoleApi} />} />
              <Route path="/debugger" element={<ProjectionDebuggerPage api={consoleApi} />} />
              <Route path="/failures" element={<FailureAnalysisPage api={consoleApi} />} />
              <Route path="/evaluation" element={<MemoryEvaluationPage api={consoleApi} />} />
              <Route path="/members" element={<MembersPage api={consoleApi} role={activeTenant?.role ?? 'member'} />} />
              <Route path="/settings" element={<SettingsPage tenant={activeTenant} />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}

// LoginPage pairs the memory-tree artwork with the sign-in form. The hero is
// only shown on large screens; the form stays centered on mobile.
function LoginPage() {
  return (
    <div className="auth-shell">
      <div className="auth-hero">
        <img src="/memory-tree.jpg" alt="" />
        <div className="auth-hero-caption">
          <p className="eyebrow">Mengpo</p>
          <h2 className="mt-2 text-2xl font-semibold text-white">Memory that remembers, governably.</h2>
          <p className="mt-2">Review candidates, inspect projections and evaluate retrieval quality across every tenant.</p>
        </div>
      </div>

      <div className="auth-panel">
        <div className="auth-card">
          <div className="brand">
            <span className="brand-mark" aria-hidden="true">
              孟
            </span>
            <span className="brand-text">
              <strong>Mengpo</strong>
              <small>Memory Console</small>
            </span>
          </div>
          <p className="eyebrow">Welcome back</p>
          <h1 className="mt-2 text-2xl font-semibold">Sign in required</h1>
          <p className="mt-1 text-sm text-muted">Authenticate to open the memory console.</p>
          <SignInPanel />
        </div>
      </div>
    </div>
  );
}

function useConsoleOrUndefined(): ConsoleApi | undefined {
  try {
    return useConsole();
  } catch {
    return undefined;
  }
}
