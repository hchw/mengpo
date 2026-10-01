import { useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { ProjectionDebug } from '../api/types';
import { StateView } from '../components/states';

export interface ProjectionDebuggerPageProps {
  api: ConsoleApi;
  initialQuery?: string;
}

// ProjectionDebuggerPage shows exactly why a projection looked the way it did:
// candidates, ranking reasons, exclusion reasons, budgets, provenance and any
// degradation. It never hides an excluded candidate.
export function ProjectionDebuggerPage({ api, initialQuery = '' }: ProjectionDebuggerPageProps) {
  const [query, setQuery] = useState(initialQuery);
  const [status, setStatus] = useState<'idle' | 'loading' | 'ready' | 'empty' | 'error'>('idle');
  const [error, setError] = useState<unknown>();
  const [debug, setDebug] = useState<ProjectionDebug>();

  const run = () => {
    setStatus('loading');
    api
      .debugProjection(query)
      .then((result) => {
        setDebug(result);
        setStatus(result.candidates.length === 0 ? 'empty' : 'ready');
      })
      .catch((cause: unknown) => {
        setError(cause);
        setStatus('error');
      });
  };

  return (
    <section aria-labelledby="debugger-title" className="page page--debugger">
      <h1 id="debugger-title">Projection Debugger</h1>
      <label>
        Query
        <input aria-label="query" value={query} onChange={(event) => setQuery(event.target.value)} />
      </label>
      <button onClick={run} disabled={query.trim() === ''}>
        Run projection
      </button>
      {status === 'loading' ? <StateView kind="loading" /> : null}
      {status === 'error' ? <StateView kind="error" error={error} /> : null}
      {status === 'empty' ? <StateView kind="empty" description="No candidates were recalled for this query." /> : null}
      {status === 'ready' && debug ? (
        <div className="debugger-result" data-testid="projection-debug">
          <dl>
            <dt>Mode</dt>
            <dd data-testid="debug-mode">{debug.mode}</dd>
            <dt>Reason</dt>
            <dd data-testid="debug-reason">{debug.reason}</dd>
            <dt>Cache</dt>
            <dd data-testid="debug-cache">{debug.cache_hit ? 'hit' : 'miss'}</dd>
            <dt>Budget</dt>
            <dd data-testid="debug-budget">
              candidates={debug.budget.candidates} ranking={debug.budget.ranking} tokens={debug.budget.injection_tokens}
            </dd>
            <dt>Usage</dt>
            <dd data-testid="debug-usage">
              seen={debug.usage.candidates_seen} ranked={debug.usage.candidates_ranked} injected={debug.usage.candidates_injected} tokens={debug.usage.tokens_injected}
            </dd>
          </dl>
          {debug.degraded ? (
            <div data-testid="debug-degraded">
              <h2>Degraded</h2>
              <ul>
                {debug.degraded_reasons.map((reason) => (
                  <li key={reason}>{reason}</li>
                ))}
              </ul>
            </div>
          ) : null}
          <table>
            <thead>
              <tr>
                <th>Memory</th>
                <th>Included</th>
                <th>Score</th>
                <th>Reason / Exclusion</th>
                <th>Provenance</th>
              </tr>
            </thead>
            <tbody data-testid="debug-candidates">
              {debug.candidates.map((candidate) => (
                <tr key={candidate.memory_id} data-testid={`debug-candidate-${candidate.memory_id}`}>
                  <td>{candidate.memory_id}</td>
                  <td>{candidate.included ? 'yes' : 'no'}</td>
                  <td>{candidate.score.toFixed(3)}</td>
                  <td>{candidate.included ? JSON.stringify(candidate.reason) : candidate.excluded_reason}</td>
                  <td>{candidate.provenance.join(',')}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </section>
  );
}