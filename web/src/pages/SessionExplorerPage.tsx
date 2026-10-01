import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { MemoryRecord, SessionSummary } from '../api/types';
import { StateView } from '../components/states';

export interface SessionExplorerPageProps {
  api: ConsoleApi;
}

// SessionExplorerPage lists sessions and, for the selected session, the
// session-scoped memories bound to it.
export function SessionExplorerPage({ api }: SessionExplorerPageProps) {
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [selected, setSelected] = useState<string>();
  const [memories, setMemories] = useState<MemoryRecord[]>([]);

  useEffect(() => {
    let cancelled = false;
    setStatus('loading');
    api
      .listSessions()
      .then((page) => {
        if (cancelled) {
          return;
        }
        setSessions(page.items);
        setSelected(page.items[0]?.id);
        setStatus(page.items.length === 0 ? 'empty' : 'ready');
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
  }, [api]);

  useEffect(() => {
    if (!selected) {
      return;
    }
    let cancelled = false;
    api
      .listMemories(1, 20, selected)
      .then((page) => {
        if (!cancelled) {
          setMemories(page.items);
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
    return <StateView kind="empty" description="No sessions recorded for this tenant." />;
  }
  return (
    <section aria-labelledby="session-title" className="page page--sessions">
      <h1 id="session-title">Session Explorer</h1>
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
      <div className="session-detail">
        <h2>Session memories</h2>
        {memories.length === 0 ? (
          <p data-testid="session-memories-empty">No memories in this session.</p>
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