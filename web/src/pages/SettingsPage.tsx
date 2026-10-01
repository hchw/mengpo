import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { ProviderView, Tenant } from '../api/types';
import { StateView } from '../components/states';

export interface SettingsPageProps {
  tenant?: Tenant;
  activeScope?: 'user-global' | 'session';
  onScopeChange?: (scope: 'user-global' | 'session') => void;
  api?: ConsoleApi;
  role?: string;
}

// SettingsPage shows the active tenant context, console preferences, and the
// tenant's model provider configuration. Provider writes are admin-only; the
// secret is write-only and is never displayed once saved.
export function SettingsPage({ tenant, activeScope = 'user-global', onScopeChange, api, role }: SettingsPageProps) {
  const isAdmin = role === 'owner' || role === 'tenant-admin';
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
      {api ? <ProvidersSection api={api} isAdmin={isAdmin} /> : null}
    </section>
  );
}

interface ProviderFormState {
  enabled: boolean;
  base_url: string;
  model: string;
  api_key: string;
}

function emptyForm(): ProviderFormState {
  return { enabled: false, base_url: '', model: '', api_key: '' };
}

function ProvidersSection({ api, isAdmin }: { api: ConsoleApi; isAdmin: boolean }) {
  const [view, setView] = useState<ProviderView>();
  const [form, setForm] = useState<ProviderFormState>(emptyForm());
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [saved, setSaved] = useState<string>();
  const [testResult, setTestResult] = useState<string>();

  useEffect(() => {
    let cancelled = false;
    setStatus('loading');
    api
      .getProvider()
      .then((value) => {
        if (cancelled) return;
        setView(value);
        setForm({ enabled: value.enabled, base_url: value.base_url, model: value.model, api_key: '' });
        setStatus('ready');
      })
      .catch((cause) => {
        if (cancelled) return;
        setError(cause);
        setStatus('error');
      });
    return () => {
      cancelled = true;
    };
  }, [api]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error' || !view) {
    return <StateView kind="error" error={error} />;
  }

  const save = async () => {
    setSaved(undefined);
    try {
      const updated = await api.updateProvider(form);
      setView(updated);
      setForm({ enabled: updated.enabled, base_url: updated.base_url, model: updated.model, api_key: '' });
      setSaved('saved');
    } catch (cause) {
      setSaved(cause instanceof Error ? cause.message : 'save failed');
    }
  };

  const test = async () => {
    setTestResult(undefined);
    try {
      const result = await api.testProvider(form);
      setTestResult(result.ok ? `ok (${result.latency_ms} ms)` : `failed: ${result.error}`);
    } catch (cause) {
      setTestResult(cause instanceof Error ? cause.message : 'test failed');
    }
  };

  return (
    <section aria-labelledby="providers-title" className="panel" data-testid="providers-section">
      <h2 id="providers-title">Memory LLM provider</h2>
      <dl data-testid="provider-summary">
        <dt>Source</dt>
        <dd data-testid="provider-source">{view.source}</dd>
        <dt>Key</dt>
        <dd data-testid="provider-secret">{view.has_secret ? view.secret_hint || 'configured' : 'not configured'}</dd>
      </dl>
      {isAdmin ? (
        <form
          className="provider-form"
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <label>
            <input
              type="checkbox"
              aria-label="enabled"
              checked={form.enabled}
              onChange={(event) => setForm({ ...form, enabled: event.target.checked })}
            />
            Enabled
          </label>
          <label>
            Base URL
            <input
              aria-label="base_url"
              value={form.base_url}
              onChange={(event) => setForm({ ...form, base_url: event.target.value })}
            />
          </label>
          <label>
            Model
            <input aria-label="model" value={form.model} onChange={(event) => setForm({ ...form, model: event.target.value })} />
          </label>
          <label>
            API key
            <input
              aria-label="api_key"
              type="password"
              placeholder={view.has_secret ? 'leave blank to keep' : ''}
              value={form.api_key}
              onChange={(event) => setForm({ ...form, api_key: event.target.value })}
            />
          </label>
          <div className="provider-form__actions">
            <button type="submit">Save</button>
            <button type="button" onClick={() => void test()}>
              Test connection
            </button>
          </div>
          {saved ? <p data-testid="provider-save-status">{saved}</p> : null}
          {testResult ? <p data-testid="provider-test-status">{testResult}</p> : null}
        </form>
      ) : (
        <p data-testid="provider-readonly">Only a tenant administrator can change the provider.</p>
      )}
    </section>
  );
}
