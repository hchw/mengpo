import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { ScheduleStatus } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface TriggerSchedulesDialogProps {
  api: ConsoleApi;
  schedules: ScheduleStatus[];
  onClose: () => void;
  /** Invoked after a request is accepted so the page can refresh its runs. */
  onTriggered: () => void;
}

function formatTime(value: string): string {
  if (!value) return '—';
  return new Date(value).toLocaleString();
}

// TriggerSchedulesDialog is the single curation entry point: the page has one
// button, and every manual run (one task, or all of them) starts here. The
// request is accepted by the API and executed by the worker's scheduler on its
// next tick, so the dialog reports "requested" rather than a finished result.
export function TriggerSchedulesDialog({ api, schedules, onClose, onTriggered }: TriggerSchedulesDialogProps) {
  const { t, tEnum } = useI18n();
  const [requested, setRequested] = useState<Record<string, boolean>>({});
  const [pending, setPending] = useState<string>();
  const [error, setError] = useState<unknown>();

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const trigger = async (name: string) => {
    setPending(name);
    setError(undefined);
    try {
      await api.triggerSchedule(name);
      setRequested((current) => ({ ...current, [name]: true }));
      onTriggered();
    } catch (cause) {
      setError(cause);
    } finally {
      setPending(undefined);
    }
  };

  const triggerAll = async () => {
    for (const schedule of schedules) {
      await trigger(schedule.name);
    }
  };

  return (
    <div className="modal" role="dialog" aria-modal="true" aria-labelledby="curation-trigger-title" onClick={onClose}>
      <div className="modal__panel" onClick={(event) => event.stopPropagation()}>
        <header className="modal__header">
          <h2 id="curation-trigger-title" className="modal__title">
            {t('curation.triggerTitle')}
          </h2>
          <button type="button" className="modal__close" aria-label={t('curation.detail.close')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal__body" data-testid="curation-trigger-dialog">
          <p className="modal__note">{t('curation.triggerHint')}</p>
          {error ? <StateView kind="error" error={error} /> : null}
          {schedules.length === 0 ? (
            <p className="modal__note">{t('curation.triggerEmpty')}</p>
          ) : (
            <>
              <ul className="trigger-list">
                {schedules.map((schedule) => {
                  const label = tEnum('curation.schedule', schedule.name);
                  return (
                    <li key={schedule.name} className="trigger-row" data-testid={`curation-trigger-row-${schedule.name}`}>
                      <div className="trigger-row__meta">
                        <strong>{label}</strong>
                        <small>
                          {schedule.cadence_seconds}s · {tEnum('curation.scheduleStatus', schedule.last_status)} · {formatTime(schedule.last_run_at)}
                        </small>
                      </div>
                      {requested[schedule.name] ? (
                        <span className="trigger-row__done" data-testid={`curation-requested-${schedule.name}`}>
                          {t('curation.requested')}
                        </span>
                      ) : (
                        <button
                          type="button"
                          className="btn"
                          data-testid={`curation-run-${schedule.name}`}
                          aria-label={`${t('curation.run')} ${label}`}
                          disabled={pending === schedule.name}
                          onClick={() => void trigger(schedule.name)}
                        >
                          {t('curation.run')}
                        </button>
                      )}
                    </li>
                  );
                })}
              </ul>
              <div className="trigger-actions">
                <button type="button" className="btn btn-primary" data-testid="curation-run-all" disabled={pending !== undefined} onClick={() => void triggerAll()}>
                  {t('curation.runAll')}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
