import type { ReactNode } from 'react';
import type { ApiClientError } from '../api/client';
import { describeError } from '../api/client';

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
  loading: { title: 'Loading', description: 'Fetching tenant-scoped data…' },
  empty: { title: 'Nothing here yet', description: 'No records match this view.' },
  error: { title: 'Request failed', description: 'The memory service could not complete this request.' },
  permission: { title: 'Permission required', description: 'Your tenant role does not allow this action.' },
  conflict: { title: 'Conflicting change', description: 'Someone else changed this record. Review and retry.' },
  stale: { title: 'Stale data', description: 'This view is out of date; refresh to continue.' },
  'tenant-switch': { title: 'Switching tenant', description: 'Loading data for the selected tenant…' },
  rollback: { title: 'Change reverted', description: 'The last optimistic change was rolled back.' },
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
  const copy = DEFAULT_COPY[kind];
  const details = error ? describeError(error as ApiClientError) : undefined;
  return (
    <section role={kind === 'error' ? 'alert' : 'status'} className={`state state--${kind}`} aria-live="polite">
      <h2>{title ?? copy.title}</h2>
      <p>{description ?? details?.message ?? copy.description}</p>
      {kind === 'error' && details ? <code data-testid="error-code">{details.code}</code> : null}
      {(retryable ?? details?.retryable) && onRetry ? <button onClick={onRetry}>Retry</button> : null}
      {children}
    </section>
  );
}

export function stillLoading(kind: StateKind): boolean {
  return kind === 'loading' || kind === 'tenant-switch';
}
