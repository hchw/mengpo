import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { MemoryRecord, MemoryStatus } from '../api/types';
import { MemoryCard } from '../components/MemoryCard';
import { Pagination } from '../components/Pagination';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';
import { MemoryDetailDialog } from './MemoryDetailDialog';

const PAGE_SIZE = 20;
const STALE_AFTER_MS = 7 * 24 * 60 * 60 * 1000;

export interface BackgroundMemoryPageProps {
  api: ConsoleApi;
  isStale?: (updatedAt: string, now: number) => boolean;
  now?: () => number;
}

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
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [detailId, setDetailId] = useState<string>();

  const load = useCallback(() => {
    setStatus('loading');
    api
      .listMemories(page, PAGE_SIZE)
      .then((result) => {
        setMemories(result.items);
        setTotal(result.total);
        setStatus(result.total === 0 ? 'empty' : 'ready');
      })
      .catch((cause: unknown) => {
        setError(cause);
        setStatus('error');
      });
  }, [api, page]);

  useEffect(() => {
    load();
  }, [load]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} onRetry={load} />;
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
        <ul data-testid="memory-list" className="memory-list">
          {visible.map((memory) => {
            const stale = now() - new Date(memory.updated_at).getTime() > STALE_AFTER_MS;
            return <MemoryCard key={memory.id} memory={memory} stale={stale} onOpen={setDetailId} />;
          })}
        </ul>
      )}
      <Pagination
        page={page}
        pageSize={PAGE_SIZE}
        total={total}
        onPageChange={setPage}
        testId="memory-pagination"
        statusTestId="memory-pagination-status"
      />
      {detailId ? <MemoryDetailDialog api={api} memoryId={detailId} onClose={() => setDetailId(undefined)} /> : null}
    </section>
  );
}
