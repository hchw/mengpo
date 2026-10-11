import { useEffect } from 'react';
import type { AnalysisRunDetail } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface AnalysisRunDetailDialogProps {
  detail?: AnalysisRunDetail;
  loading: boolean;
  error?: unknown;
  onClose: () => void;
}

// pretty re-indents a JSON text preview; if the payload is not JSON (a rendered
// template, a plain log line) it is shown verbatim.
function pretty(text: string): string {
  if (!text) return '—';
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

function formatTime(value: string): string {
  if (!value) return '—';
  return new Date(value).toLocaleString();
}

// AnalysisRunDetailDialog shows what a curation task consumed (its source
// events) and produced (memory nodes plus any structured result). Content is
// already truncated server-side, so very large tool payloads stay bounded.
export function AnalysisRunDetailDialog({ detail, loading, error, onClose }: AnalysisRunDetailDialogProps) {
  const { t, tEnum } = useI18n();

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const run = detail?.run;

  return (
    <div className="modal" role="dialog" aria-modal="true" aria-labelledby="run-detail-title" onClick={onClose}>
      <div className="modal__panel" onClick={(event) => event.stopPropagation()}>
        <header className="modal__header">
          <h2 id="run-detail-title" className="modal__title">
            {t('curation.detail.title')}
            {run ? ` · ${tEnum('curation.task', run.task_type)}` : ''}
          </h2>
          <button type="button" className="modal__close" aria-label={t('curation.detail.close')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal__body" data-testid="run-detail">
          {loading ? (
            <StateView kind="loading" />
          ) : error ? (
            <StateView kind="error" error={error} />
          ) : detail && run ? (
            <RunDetailBody detail={detail} />
          ) : null}
        </div>
      </div>
    </div>
  );
}

function RunDetailBody({ detail }: { detail: AnalysisRunDetail }) {
  const { t, tEnum } = useI18n();
  const { run, inputs, outputs, result, truncated } = detail;
  const hasResult = result !== null && result !== undefined && result !== '';
  const resultText = typeof result === 'string' ? result : JSON.stringify(result, null, 2);

  return (
    <>
      <dl data-testid="run-detail-meta">
        <dt>{t('curation.trigger')}</dt>
        <dd>{tEnum('curation.trigger', run.trigger)}</dd>
        <dt>{t('curation.status')}</dt>
        <dd data-status={run.status}>
          {tEnum('curation.status', run.status)}
          {run.degraded_reason ? ` (${tEnum('curation.degraded', run.degraded_reason)})` : ''}
        </dd>
        <dt>{t('curation.model')}</dt>
        <dd>{run.model || run.provider || '—'}</dd>
        <dt>{t('curation.promptVersion')}</dt>
        <dd>{run.prompt_version || '—'}</dd>
        <dt>{t('curation.session')}</dt>
        <dd>{run.session_id || '—'}</dd>
        <dt>{t('curation.tokens')}</dt>
        <dd>{run.tokens_prompt + run.tokens_completion}</dd>
        <dt>{t('curation.latency')}</dt>
        <dd>{run.latency_ms} ms</dd>
        <dt>{t('curation.when')}</dt>
        <dd>{formatTime(run.created_at)}</dd>
      </dl>

      <section data-testid="run-detail-result">
        <h3>{t('curation.detail.result')}</h3>
        {hasResult ? (
          <div className="preview">
            <pre>{resultText}</pre>
          </div>
        ) : (
          <p className="modal__note">{t('curation.detail.noResult')}</p>
        )}
      </section>

      <section data-testid="run-detail-inputs">
        <h3>
          {t('curation.detail.inputs')} ({inputs.length})
        </h3>
        {inputs.length === 0 ? (
          <p className="modal__note">{t('curation.detail.noInputs')}</p>
        ) : (
          inputs.map((input) => (
            <article className="preview" key={input.id} data-testid={`run-input-${input.id}`}>
              <header>
                <span className="badge">{input.message_type || input.source_type || '—'}</span>
                <span>{formatTime(input.occurred_at)}</span>
                <span>{input.id}</span>
              </header>
              <pre>{pretty(input.payload)}</pre>
            </article>
          ))
        )}
      </section>

      <section data-testid="run-detail-outputs">
        <h3>
          {t('curation.detail.outputs')} ({outputs.length})
        </h3>
        {outputs.length === 0 ? (
          <p className="modal__note">{t('curation.detail.noOutputs')}</p>
        ) : (
          outputs.map((output) => (
            <article className="preview" key={output.id} data-testid={`run-output-${output.id}`}>
              <header>
                <span className="badge">{output.status}</span>
                <span className="badge">{output.memory_type}</span>
                <span className="badge">{output.scope_type}</span>
                <span>{t('curation.confidence', { value: output.confidence.toFixed(2) })}</span>
                <span>{output.id}</span>
              </header>
              <pre>{output.content_text || '—'}</pre>
            </article>
          ))
        )}
      </section>

      {truncated ? <p className="modal__note">{t('curation.detail.truncated')}</p> : null}
    </>
  );
}
