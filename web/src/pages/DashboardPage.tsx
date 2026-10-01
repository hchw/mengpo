import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { EvaluationSnapshot, MemoryRecord, SessionSummary } from '../api/types';
import { StateView } from '../components/states';

export interface DashboardPageProps {
  api: ConsoleApi;
}

interface DashboardData {
  sessions: SessionSummary[];
  memories: MemoryRecord[];
  evaluation: EvaluationSnapshot;
}

// DashboardPage summarizes the tenant: recent sessions, memory counts, and the
// latest evaluation snapshot. It is read-only.
export function DashboardPage({ api }: DashboardPageProps) {
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [data, setData] = useState<DashboardData>();
  const [nonce, setNonce] = useState(0);

  const load = useCallback(() => {
    setStatus('loading');
    Promise.all([api.listSessions(1, 5), api.listMemories(1, 5), api.getEvaluation()])
      .then(([sessions, memories, evaluation]) => {
        if (sessions.total === 0 && memories.total === 0) {
          setStatus('empty');
          return;
        }
        setData({ sessions: sessions.items, memories: memories.items, evaluation });
        setStatus('ready');
      })
      .catch((cause: unknown) => {
        setError(cause);
        setStatus('error');
      });
  }, [api]);

  useEffect(() => {
    load();
  }, [load, nonce]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} onRetry={() => setNonce((value) => value + 1)} />;
  }
  if (status === 'empty' || !data) {
    return <StateView kind="empty" description="No sessions or memories for this tenant yet." />;
  }
  return (
    <section aria-labelledby="dashboard-title" className="page page--dashboard">
      <h1 id="dashboard-title">Dashboard</h1>
      <div className="cards" data-testid="metric-cards">
        <article className="card">
          <h2>Recent sessions</h2>
          <p data-testid="session-count">{data.sessions.length}</p>
          <ul>
            {data.sessions.map((session) => (
              <li key={session.id}>{session.title || session.id}</li>
            ))}
          </ul>
        </article>
        <article className="card">
          <h2>Recent memories</h2>
          <p data-testid="memory-count">{data.memories.length}</p>
          <ul>
            {data.memories.map((memory) => (
              <li key={memory.id}>{memory.content_summary}</li>
            ))}
          </ul>
        </article>
        <article className="card">
          <h2>Evaluation</h2>
          <p data-testid="retrieval-precision">Retrieval precision: {(data.evaluation.retrieval_precision * 100).toFixed(1)}%</p>
          <p data-testid="cache-hit-rate">Cache hit rate: {(data.evaluation.cache_hit_rate * 100).toFixed(1)}%</p>
        </article>
      </div>
    </section>
  );
}