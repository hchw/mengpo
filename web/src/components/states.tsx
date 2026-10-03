import type { ReactNode } from 'react';
import type { ApiClientError } from '../api/client';
import { describeError } from '../api/client';
import { useI18n } from '../i18n';

export type StateKind =
  | 'loading'
  | 'empty'
  | 'error'
  | 'permission'
  | 'conflict'
  | 'stale'
  | 'tenant-switch'
  | 'rollback';

const DEFAULT_COPY: Record<StateKind, { title: string; description: string }> = {
  loading: { title: 'state.loading.title', description: 'state.loading.desc' },
  empty: { title: 'state.empty.title', description: 'state.empty.desc' },
  error: { title: 'state.error.title', description: 'state.error.desc' },
  permission: { title: 'state.permission.title', description: 'state.permission.desc' },
  conflict: { title: 'state.conflict.title', description: 'state.conflict.desc' },
  stale: { title: 'state.stale.title', description: 'state.stale.desc' },
  'tenant-switch': { title: 'state.tenant-switch.title', description: 'state.tenant-switch.desc' },
  rollback: { title: 'state.rollback.title', description: 'state.rollback.desc' },
};

export interface StateViewProps {
  kind: StateKind;
  title?: string;
  description?: string;
  error?: unknown;
  retryable?: boolean;
  onRetry?: () => void;
  children?: ReactNode;
}

export function StateView({ kind, title, description, error, retryable, onRetry, children }: StateViewProps) {
  const { t } = useI18n();
  const copy = DEFAULT_COPY[kind];
  const details = error ? describeError(error as ApiClientError) : undefined;
  return (
    <section role={kind === 'error' ? 'alert' : 'status'} className={`state state--${kind}`} aria-live="polite">
      <h2>{title ?? t(copy.title)}</h2>
      <p>{description ?? details?.message ?? t(copy.description)}</p>
      {kind === 'error' && details ? <code data-testid="error-code">{details.code}</code> : null}
      {(retryable ?? details?.retryable) && onRetry ? <button onClick={onRetry}>{t('state.retry')}</button> : null}
      {children}
    </section>
  );
}

export function stillLoading(kind: StateKind): boolean {
  return kind === 'loading' || kind === 'tenant-switch';
}
