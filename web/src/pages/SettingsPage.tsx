import type { Tenant } from '../api/types';

export interface SettingsPageProps {
  tenant?: Tenant;
  activeScope?: 'user-global' | 'session';
  onScopeChange?: (scope: 'user-global' | 'session') => void;
}

// SettingsPage shows the active tenant context and the console preferences that
// affect how projections are requested.
export function SettingsPage({ tenant, activeScope = 'user-global', onScopeChange }: SettingsPageProps) {
  return (
    <section aria-labelledby="settings-title" className="page page--settings">
      <h1 id="settings-title">Settings</h1>
      <dl>
        <dt>Active tenant</dt>
        <dd data-testid="settings-tenant">{tenant ? `${tenant.name} (${tenant.id})` : 'none'}</dd>
        <dt>Role</dt>
        <dd data-testid="settings-role">{tenant?.role ?? 'none'}</dd>
      </dl>
      <div role="group" aria-label="default scope">
        <button aria-pressed={activeScope === 'user-global'} onClick={() => onScopeChange?.('user-global')}>
          user-global
        </button>
        <button aria-pressed={activeScope === 'session'} onClick={() => onScopeChange?.('session')}>
          session
        </button>
      </div>
    </section>
  );
}