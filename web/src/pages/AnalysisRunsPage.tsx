import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { AnalysisRun, ScheduleStatus } from '../api/types';
import { StateView } from '../components/states';

export interface AnalysisRunsPageProps {
  api: ConsoleApi;
}

function formatTime(value: string): string {
  if (!value) return '—';
  return new Date(value).toISOString();
}

// AnalysisRunsPage is the curation record: every Memory LLM run plus the
// tenant's own curation schedule, so operators can see what ran and when it
// next runs without a separate scheduler page.
export function AnalysisRunsPage({ api }: AnalysisRunsPageProps) {
  const [status, setStatus] = useState<'loading' | 'empty' | 'ready' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [runs, setRuns] = useState<AnalysisRun[]>([]);
  const [schedule, setSchedule] = useState<ScheduleStatus>();

  useEffect(() => {
    let cancelled = false;
    Promise.all([api.listAnalysisRuns(1, 50), api.listSchedules()])
      .then(([runsPage, schedules]) => {
        if (cancelled) return;
        setRuns(runsPage.items ?? []);
        setSchedule((schedules.items ?? [])[0]);
        setStatus((runsPage.items ?? []).length === 0 ? 'empty' : 'ready');
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
  if (status === 'error') {
    return <StateView kind="error" error={error} />;
  }

  const failing = schedule && schedule.last_status === 'error';

  return (
    <section aria-labelledby="analysis-runs-title" className="page page--runs">
      <h1 id="analysis-runs-title">Curation</h1>
      {schedule ? (
        <dl data-testid="curation-schedule" data-failing={failing ? 'true' : 'false'}>
          <dt>Cadence</dt>
          <dd data-testid="schedule-cadence">{schedule.cadence_seconds}s</dd>
          <dt>Next run</dt>
          <dd data-testid="schedule-next">{formatTime(schedule.next_run_at)}</dd>
          <dt>Last run</dt>
          <dd data-testid="schedule-last">{formatTime(schedule.last_run_at)}</dd>
          <dt>Last result</dt>
          <dd data-testid="schedule-status">
            {schedule.last_status}
            {failing && schedule.last_error ? `: ${schedule.last_error}` : ''}
          </dd>
        </dl>
      ) : (
        <p data-testid="curation-schedule-empty">No curation schedule is configured.</p>
      )}

      {status === 'empty' ? (
        <StateView kind="empty" description="No curation runs yet." />
      ) : (
        <table data-testid="analysis-runs">
          <thead>
            <tr>
              <th>Task</th>
              <th>Trigger</th>
              <th>Model</th>
              <th>Status</th>
              <th>Candidates</th>
              <th>Tokens</th>
              <th>Latency</th>
              <th>When</th>
            </tr>
          </thead>
          <tbody>
            {runs.map((run) => (
              <tr key={run.id} data-testid={`analysis-run-${run.id}`}>
                <td>{run.task_type}</td>
                <td>{run.trigger}</td>
                <td>{run.model || run.provider}</td>
                <td data-status={run.status}>
                  {run.status}
                  {run.degraded_reason ? ` (${run.degraded_reason})` : ''}
                </td>
                <td>{run.candidate_count}</td>
                <td>{run.tokens_prompt + run.tokens_completion}</td>
                <td>{run.latency_ms} ms</td>
                <td>{formatTime(run.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
