import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { MemoryRecord, MemoryStatus } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface BackgroundMemoryPageProps {
  api: ConsoleApi;
  isStale?: (updatedAt: string, now: number) => boolean;
  now?: () => number;
}

const STALE_AFTER_MS = 7 * 24 * 60 * 60 * 1000;

const FILTERS: Array<{ labelKey: string; value: 'all' | MemoryStatus }> = [
  { labelKey: 'background.filter.all', value: 'all' },
  { labelKey: 'background.filter.active', value: 'active' },
  { labelKey: 'background.filter.stable', value: 'stable' },
  { labelKey: 'background.filter.conflicted', value: 'conflicted' },
];

// BackgroundMemoryPage lists long-lived (non-session) memories with a status
// filter and a stale indicator for memories that have not been reinforced.
export function BackgroundMemoryPage({ api, now = () => Date.now() }: BackgroundMemoryPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [memories, setMemories] = useState<MemoryRecord[]>([]);
  const [filter, setFilter] = useState<'all' | MemoryStatus>('all');

  const load = useCallback(() => {
    setStatus('loading');
    api
      .listMemories(1, 50)
      .then((page) => {
        setMemories(page.items);
        setStatus(page.items.length === 0 ? 'empty' : 'ready');
      })
      .catch((cause: unknown) => {
        setError(cause);
        setStatus('error');
      });
  }, [api]);

  useEffect(() => {
    load();
  }, [load]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description={t('background.empty')} />;
  }
  const visible = filter === 'all' ? memories : memories.filter((memory) => memory.status === filter);
  return (
    <section aria-labelledby="background-title" className="page page--background">
      <h1 id="background-title">{t('background.title')}</h1>
      <div role="group" aria-label={t('background.filterAria')} className="filters">
        {FILTERS.map((option) => (
          <button key={option.value} aria-pressed={filter === option.value} onClick={() => setFilter(option.value)}>
            {t(option.labelKey)}
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <p data-testid="filtered-empty">{t('background.noneWithStatus', { status: filter })}</p>
      ) : (
        <ul data-testid="memory-list">
          {visible.map((memory) => {
            const stale = now() - new Date(memory.updated_at).getTime() > STALE_AFTER_MS;
            return (
              <li key={memory.id} data-testid={`memory-${memory.id}`}>
                <span>{memory.content_summary}</span>
                <span data-testid={`memory-status-${memory.id}`}>{memory.status}</span>
                {stale ? <span data-testid={`memory-stale-${memory.id}`}>{t('background.stale')}</span> : null}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
