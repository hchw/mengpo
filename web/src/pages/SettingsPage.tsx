import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { ProviderView, Tenant } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

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
  const { t } = useI18n();
  const isAdmin = role === 'owner' || role === 'tenant-admin';
  return (
    <section aria-labelledby="settings-title" className="page page--settings">
      <h1 id="settings-title">{t('settings.title')}</h1>
      <dl>
        <dt>{t('settings.activeTenant')}</dt>
        <dd data-testid="settings-tenant">{tenant ? `${tenant.name} (${tenant.id})` : t('settings.none')}</dd>
        <dt>{t('settings.role')}</dt>
        <dd data-testid="settings-role">{tenant?.role ?? t('settings.none')}</dd>
      </dl>
      <div role="group" aria-label={t('settings.defaultScope')}>
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
  const { t } = useI18n();
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
      setSaved(cause instanceof Error ? cause.message : 'failed');
    }
  };

  const test = async () => {
    setTestResult(undefined);
    try {
      const result = await api.testProvider(form);
      setTestResult(result.ok ? t('settings.provider.testOk', { latency: result.latency_ms }) : t('settings.provider.testFailedPrefix', { error: result.error ?? '' }));
    } catch (cause) {
      setTestResult(cause instanceof Error ? cause.message : t('settings.provider.testFailed'));
    }
  };

  return (
    <section aria-labelledby="providers-title" className="panel" data-testid="providers-section">
      <h2 id="providers-title">{t('settings.provider.title')}</h2>
      <dl data-testid="provider-summary">
        <dt>{t('settings.provider.source')}</dt>
        <dd data-testid="provider-source">{view.source}</dd>
        <dt>{t('settings.provider.key')}</dt>
        <dd data-testid="provider-secret">
          {view.has_secret ? view.secret_hint || t('settings.provider.configured') : t('settings.provider.notConfigured')}
        </dd>
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
            {t('settings.provider.enabled')}
          </label>
          <label>
            {t('settings.provider.baseUrl')}
            <input
              aria-label="base_url"
              value={form.base_url}
              onChange={(event) => setForm({ ...form, base_url: event.target.value })}
            />
          </label>
          <label>
            {t('settings.provider.model')}
            <input aria-label="model" value={form.model} onChange={(event) => setForm({ ...form, model: event.target.value })} />
          </label>
          <label>
            {t('settings.provider.apiKey')}
            <input
              aria-label="api_key"
              type="password"
              placeholder={view.has_secret ? t('settings.provider.leaveBlank') : ''}
              value={form.api_key}
              onChange={(event) => setForm({ ...form, api_key: event.target.value })}
            />
          </label>
          <div className="provider-form__actions">
            <button type="submit">{t('settings.provider.save')}</button>
            <button type="button" onClick={() => void test()}>
              {t('settings.provider.test')}
            </button>
          </div>
          {saved ? <p data-testid="provider-save-status">{saved === 'saved' ? t('settings.provider.saved') : saved}</p> : null}
          {testResult ? <p data-testid="provider-test-status">{testResult}</p> : null}
        </form>
      ) : (
        <p data-testid="provider-readonly">{t('settings.provider.onlyAdmin')}</p>
      )}
    </section>
  );
}
