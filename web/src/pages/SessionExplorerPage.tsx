import { useEffect, useMemo, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { MemoryRecord, SessionSummary } from '../api/types';
import { MemoryCard } from '../components/MemoryCard';
import { Pagination } from '../components/Pagination';
import { StateView } from '../components/states';
import { formatDateTime } from '../format';
import { useI18n } from '../i18n';
import { MemoryDetailDialog } from './MemoryDetailDialog';

const PAGE_SIZE = 20;

export interface SessionExplorerPageProps {
  api: ConsoleApi;
}

interface SessionGroup {
  title: string;
  sessions: SessionSummary[];
}

// groupByTitle collapses sessions that share a title (many sessions use the
// working directory as their title) into one entry, preserving first-seen order
// so the list stays stable across refetches.
function groupByTitle(sessions: SessionSummary[]): SessionGroup[] {
  const order: string[] = [];
  const byTitle = new Map<string, SessionSummary[]>();
  for (const session of sessions) {
    const title = session.title || session.id;
    if (!byTitle.has(title)) {
      byTitle.set(title, []);
      order.push(title);
    }
    byTitle.get(title)?.push(session);
  }
  return order.map((title) => ({ title, sessions: byTitle.get(title) ?? [] }));
}

// SessionExplorerPage is a drawer drill-down: titles expand in place to their
// sessions, and selecting a session slides a drawer in from the right with that
// session's working memory. Nothing is laid out in fixed side-by-side columns,
// so each level keeps its full width.
export function SessionExplorerPage({ api }: SessionExplorerPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [selectedTitle, setSelectedTitle] = useState<string>();
  const [selected, setSelected] = useState<string>();
  const [memories, setMemories] = useState<MemoryRecord[]>([]);
  const [memoryPage, setMemoryPage] = useState(1);
  const [memoryTotal, setMemoryTotal] = useState(0);
  const [memoryError, setMemoryError] = useState<unknown>();
  const [detailId, setDetailId] = useState<string>();

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

  const groups = useMemo(() => groupByTitle(sessions), [sessions]);

  // Keep the expanded title valid as pages load, defaulting to the first group.
  useEffect(() => {
    if (groups.length === 0) {
      setSelectedTitle(undefined);
      return;
    }
    setSelectedTitle((current) => (current && groups.some((group) => group.title === current) ? current : groups[0].title));
  }, [groups]);

  useEffect(() => {
    setMemoryPage(1);
  }, [selected]);

  useEffect(() => {
    if (!selected) {
      setMemories([]);
      setMemoryTotal(0);
      return;
    }
    let cancelled = false;
    setMemoryError(undefined);
    api
      .listMemories(memoryPage, PAGE_SIZE, selected)
      .then((result) => {
        if (!cancelled) {
          setMemories(result.items);
          setMemoryTotal(result.total);
        }
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setMemoryError(cause);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [api, selected, memoryPage]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description={t('sessions.empty')} />;
  }

  const toggleTitle = (title: string) => {
    if (title === selectedTitle) {
      return;
    }
    setSelectedTitle(title);
    // Switching titles closes the drawer so the next session is picked from the
    // group the operator just opened.
    setSelected(undefined);
  };

  return (
    <section aria-labelledby="session-title" className="page page--sessions">
      <h1 id="session-title">{t('sessions.title')}</h1>
      <ul className="title-accordion" data-testid="session-title-list">
        {groups.map((group) => {
          const expanded = selectedTitle === group.title;
          return (
            <li key={group.title} className="title-item" data-expanded={expanded ? 'true' : 'false'}>
              <button type="button" className="title-item__button" aria-expanded={expanded} onClick={() => toggleTitle(group.title)}>
                <span className="title-item__caret" aria-hidden="true">
                  ▸
                </span>
                <span className="title-item__name">{group.title}</span>
                <span className="badge">{t('sessions.sessionCount', { count: group.sessions.length })}</span>
              </button>
              {expanded ? (
                <ul className="session-list" data-testid="session-list">
                  {group.sessions.map((session) => (
                    <li key={session.id} className="session-item">
                      <button
                        type="button"
                        className="session-item__button"
                        aria-pressed={selected === session.id}
                        onClick={() => setSelected(session.id)}
                      >
                        <span className="session-item__id">{session.id}</span>
                        <span data-testid={`session-status-${session.id}`}>{session.status}</span>
                        <span className="session-item__time" data-testid={`session-updated-${session.id}`}>
                          {formatDateTime(session.updated_at)}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          );
        })}
      </ul>
      <Pagination
        page={page}
        pageSize={PAGE_SIZE}
        total={total}
        onPageChange={setPage}
        testId="session-pagination"
        statusTestId="session-pagination-status"
      />

      {selected ? (
        <div className="drawer" role="dialog" aria-modal="true" aria-label={t('sessions.memories')} onClick={() => setSelected(undefined)}>
          <aside className="drawer__panel" onClick={(event) => event.stopPropagation()}>
            <header className="drawer__header">
              <div className="drawer__heading">
                <h2>{t('sessions.memories')}</h2>
                <p className="drawer__subtitle">{selected}</p>
              </div>
              <button type="button" className="modal__close" aria-label={t('sessions.closeDrawer')} onClick={() => setSelected(undefined)}>
                ×
              </button>
            </header>
            <div className="drawer__body">
              {memoryError ? (
                <StateView kind="error" error={memoryError} />
              ) : memories.length === 0 ? (
                <p data-testid="session-memories-empty">{t('sessions.none')}</p>
              ) : (
                <ul className="memory-list" data-testid="session-memories">
                  {memories.map((memory) => (
                    <MemoryCard key={memory.id} memory={memory} onOpen={setDetailId} />
                  ))}
                </ul>
              )}
              <Pagination
                page={memoryPage}
                pageSize={PAGE_SIZE}
                total={memoryTotal}
                onPageChange={setMemoryPage}
                testId="session-memory-pagination"
                statusTestId="session-memory-pagination-status"
              />
            </div>
          </aside>
        </div>
      ) : null}

      {detailId ? (
        <MemoryDetailDialog api={api} memoryId={detailId} sessionId={selected} onClose={() => setDetailId(undefined)} />
      ) : null}
    </section>
  );
}
