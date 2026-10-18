import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { AnalysisRun, AnalysisRunDetail, ScheduleStatus } from '../api/types';
import { Pagination } from '../components/Pagination';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';
import { AnalysisRunDetailDialog } from './AnalysisRunDetailDialog';
import { TriggerSchedulesDialog } from './TriggerSchedulesDialog';

const PAGE_SIZE = 20;

export interface AnalysisRunsPageProps {
  api: ConsoleApi;
  role?: string;
}

function formatTime(value: string): string {
  if (!value) return '—';
  return new Date(value).toISOString();
}

// AnalysisRunsPage is the curation record: every Memory LLM run plus the
// tenant's own curation schedule, so operators can see what ran and when it
// next runs without a separate scheduler page.
export function AnalysisRunsPage({ api, role }: AnalysisRunsPageProps) {
  const { t, tEnum } = useI18n();
  // Manual triggers write the schedule, so they match the provider-write gate:
  // tenant owners and administrators only.
  const isAdmin = role === undefined || role === 'owner' || role === 'admin' || role === 'administrator' || role === 'tenant-admin';
  const [status, setStatus] = useState<'loading' | 'empty' | 'ready' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [runs, setRuns] = useState<AnalysisRun[]>([]);
  const [schedule, setSchedule] = useState<ScheduleStatus>();
  const [schedules, setSchedules] = useState<ScheduleStatus[]>([]);
  const [triggerOpen, setTriggerOpen] = useState(false);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [nonce, setNonce] = useState(0);
  const [detailRunId, setDetailRunId] = useState<string>();
  const [detail, setDetail] = useState<AnalysisRunDetail>();
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<unknown>();

  useEffect(() => {
    let cancelled = false;
    setStatus('loading');
    Promise.all([api.listAnalysisRuns(page, PAGE_SIZE), api.listSchedules()])
      .then(([runsPage, schedules]) => {
        if (cancelled) return;
        const items = runsPage.items ?? [];
        setRuns(items);
        setTotal(runsPage.total ?? items.length);
        setSchedule((schedules.items ?? [])[0]);
        setSchedules(schedules.items ?? []);
        setStatus((runsPage.total ?? items.length) === 0 ? 'empty' : 'ready');
      })
      .catch((cause) => {
        if (cancelled) return;
        setError(cause);
        setStatus('error');
      });
    return () => {
      cancelled = true;
    };
  }, [api, page, nonce]);

  const openDetail = (id: string) => {
    setDetailRunId(id);
    setDetail(undefined);
    setDetailError(undefined);
    setDetailLoading(true);
    api
      .getAnalysisRunDetail(id)
      .then((value) => setDetail(value))
      .catch((cause) => setDetailError(cause))
      .finally(() => setDetailLoading(false));
  };
  const closeDetail = () => setDetailRunId(undefined);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} onRetry={() => setNonce((value) => value + 1)} />;
  }
  const failing = schedule && schedule.last_status === 'error';
  return (
    <section aria-labelledby="analysis-runs-title" className="page page--runs">
      <div className="page-head">
        <h1 id="analysis-runs-title">{t('curation.title')}</h1>
        {isAdmin ? (
          <button type="button" className="btn btn-primary" data-testid="curation-trigger-open" onClick={() => setTriggerOpen(true)}>
            {t('curation.runNow')}
          </button>
        ) : null}
      </div>
      {schedule ? (
        <dl data-testid="curation-schedule" data-failing={failing ? 'true' : 'false'}>
          <dt>{t('curation.cadence')}</dt>
          <dd data-testid="schedule-cadence">{schedule.cadence_seconds}s</dd>
          <dt>{t('curation.nextRun')}</dt>
          <dd data-testid="schedule-next">{formatTime(schedule.next_run_at)}</dd>
          <dt>{t('curation.lastRun')}</dt>
          <dd data-testid="schedule-last">{formatTime(schedule.last_run_at)}</dd>
          <dt>{t('curation.lastResult')}</dt>
          <dd data-testid="schedule-status">{`${tEnum('curation.scheduleStatus', schedule.last_status)}${failing && schedule.last_error ? `: ${schedule.last_error}` : ''}`}</dd>
        </dl>
      ) : (
        <p data-testid="curation-schedule-empty">{t('curation.noSchedule')}</p>
      )}
      {status === 'empty' ? (
        <StateView kind="empty" description={t('curation.empty')} />
      ) : (
        <>
          <table data-testid="analysis-runs">
            <thead>
              <tr>
                <th>{t('curation.task')}</th>
                <th>{t('curation.trigger')}</th>
                <th>{t('curation.model')}</th>
                <th>{t('curation.status')}</th>
                <th>{t('curation.candidates')}</th>
                <th>{t('curation.tokens')}</th>
                <th>{t('curation.latency')}</th>
                <th>{t('curation.when')}</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => (
                <tr key={run.id} data-testid={`analysis-run-${run.id}`}>
                  <td>
                    <button type="button" className="run-link" onClick={() => openDetail(run.id)}>
                      {tEnum('curation.task', run.task_type)}
                    </button>
                  </td>
                  <td>{tEnum('curation.trigger', run.trigger)}</td>
                  <td>{run.model || run.provider}</td>
                  <td data-status={run.status}>
                    {tEnum('curation.status', run.status)} {run.degraded_reason ? ` (${tEnum('curation.degraded', run.degraded_reason)})` : ''}
                  </td>
                  <td>{run.candidate_count}</td>
                  <td>{run.tokens_prompt + run.tokens_completion}</td>
                  <td>{run.latency_ms} ms</td>
                  <td>{formatTime(run.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onPageChange={setPage} />
        </>
      )}
      {detailRunId ? (
        <AnalysisRunDetailDialog detail={detail} loading={detailLoading} error={detailError} onClose={closeDetail} />
      ) : null}
      {triggerOpen ? (
        <TriggerSchedulesDialog
          api={api}
          schedules={schedules}
          onClose={() => setTriggerOpen(false)}
          onTriggered={() => setNonce((value) => value + 1)}
        />
      ) : null}
    </section>
  );
}
