import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { MemoryRecord, SessionSummary } from '../api/types';
import { Pagination } from '../components/Pagination';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

const PAGE_SIZE = 20;

export interface SessionExplorerPageProps {
  api: ConsoleApi;
}

// SessionExplorerPage lists sessions and, for the selected session, the
// session-scoped memories bound to it.
export function SessionExplorerPage({ api }: SessionExplorerPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [selected, setSelected] = useState<string>();
  const [memories, setMemories] = useState<MemoryRecord[]>([]);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setStatus('loading');
    api
      .listSessions(page, PAGE_SIZE)
      .then((result) => {
        if (cancelled) {
          return;
        }
        setSessions(result.items);
        setTotal(result.total);
        setSelected(result.items[0]?.id);
        setStatus(result.items.length === 0 ? 'empty' : 'ready');
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setError(cause);
          setStatus('error');
        }
      });
    return () => {
      cancelled = true;
    };
  }, [api, page]);

  useEffect(() => {
    if (!selected) {
      return;
    }
    let cancelled = false;
    api
      .listMemories(1, 20, selected)
      .then((result) => {
        if (!cancelled) {
          setMemories(result.items);
        }
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setError(cause);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [api, selected]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description={t('sessions.empty')} />;
  }
  return (
    <section aria-labelledby="session-title" className="page page--sessions">
      <h1 id="session-title">{t('sessions.title')}</h1>
      <ul className="session-list" data-testid="session-list">
        {sessions.map((session) => (
          <li key={session.id}>
            <button aria-pressed={selected === session.id} onClick={() => setSelected(session.id)}>
              {session.title || session.id}
            </button>
            <span data-testid={`session-status-${session.id}`}>{session.status}</span>
          </li>
        ))}
      </ul>
      <Pagination page={page} pageSize={PAGE_SIZE} total={total} onPageChange={setPage} />
      <div className="session-detail">
        <h2>{t('sessions.memories')}</h2>
        {memories.length === 0 ? (
          <p data-testid="session-memories-empty">{t('sessions.none')}</p>
        ) : (
          <ul data-testid="session-memories">
            {memories.map((memory) => (
              <li key={memory.id}>{memory.content_summary}</li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
