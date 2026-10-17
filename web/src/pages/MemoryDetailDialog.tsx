import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { AnalysisRunDetail, MemoryDetail } from '../api/types';
import { formatDateTime } from '../format';
import { useI18n } from '../i18n';
import { StateView } from '../components/states';
import { AnalysisRunDetailDialog } from './AnalysisRunDetailDialog';

export interface MemoryDetailDialogProps {
  api: ConsoleApi;
  memoryId: string;
  sessionId?: string;
  onClose: () => void;
}

// pretty re-indents a JSON text preview; anything that is not JSON is shown
// verbatim, so a plain rendered string never fails to render.
function pretty(text: string): string {
  if (!text) return '—';
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

// structuredContent renders the stored content payload, returning '' when it is
// empty so the section can show a note instead of an empty box.
function structuredContent(content: unknown): string {
  if (content === null || content === undefined) return '';
  if (typeof content === 'string') return content;
  try {
    const compact = JSON.stringify(content);
    if (compact === '{}' || compact === 'null' || compact === '""') return '';
    return JSON.stringify(content, null, 2);
  } catch {
    return '';
  }
}

// MemoryDetailDialog shows one memory with the content and raw events behind it,
// and pivots to the curated task that produced it when one is linked.
export function MemoryDetailDialog({ api, memoryId, sessionId, onClose }: MemoryDetailDialogProps) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<MemoryDetail>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>();
  const [runDetail, setRunDetail] = useState<AnalysisRunDetail>();
  const [runLoading, setRunLoading] = useState(false);
  const [runError, setRunError] = useState<unknown>();
  const [runOpen, setRunOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(undefined);
    setDetail(undefined);
    api
      .getMemory(memoryId, sessionId)
      .then((value) => {
        if (!cancelled) setDetail(value);
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [api, memoryId, sessionId]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const openRun = (runId: string) => {
    setRunOpen(true);
    setRunDetail(undefined);
    setRunError(undefined);
    setRunLoading(true);
    api
      .getAnalysisRunDetail(runId)
      .then((value) => setRunDetail(value))
      .catch((cause: unknown) => setRunError(cause))
      .finally(() => setRunLoading(false));
  };

  return (
    <>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="memory-detail-title" onClick={onClose}>
        <div className="modal__panel" onClick={(event) => event.stopPropagation()}>
          <header className="modal__header">
            <h2 id="memory-detail-title" className="modal__title">
              {t('memory.detail.title')}
            </h2>
            <button type="button" className="modal__close" aria-label={t('memory.detail.close')} onClick={onClose}>
              ×
            </button>
          </header>
          <div className="modal__body" data-testid="memory-detail">
            {loading ? (
              <StateView kind="loading" />
            ) : error ? (
              <StateView kind="error" error={error} />
            ) : detail ? (
              <MemoryDetailBody detail={detail} onOpenRun={openRun} />
            ) : null}
          </div>
        </div>
      </div>
      {runOpen ? (
        <AnalysisRunDetailDialog
          detail={runDetail}
          loading={runLoading}
          error={runError}
          onClose={() => setRunOpen(false)}
        />
      ) : null}
    </>
  );
}

function MemoryDetailBody({ detail, onOpenRun }: { detail: MemoryDetail; onOpenRun: (runId: string) => void }) {
  const { t, tEnum } = useI18n();
  const content = structuredContent(detail.content);
  const evidence = detail.evidence ?? [];

  return (
    <>
      <p className="memory-detail__summary">{detail.content_summary}</p>
      <dl data-testid="memory-detail-meta">
        <dt>{t('memory.detail.type')}</dt>
        <dd>
          <span className="badge">{detail.type}</span>
        </dd>
        <dt>{t('memory.detail.status')}</dt>
        <dd>
          <span data-testid={`memory-status-${detail.id}`}>{detail.status}</span>
        </dd>
        <dt>{t('memory.detail.scope')}</dt>
        <dd>{tEnum('memory.scope', detail.scope_type)}</dd>
        <dt>{t('memory.detail.confidence')}</dt>
        <dd>{detail.confidence.toFixed(2)}</dd>
        <dt>{t('memory.detail.created')}</dt>
        <dd>{formatDateTime(detail.created_at)}</dd>
        <dt>{t('memory.detail.updated')}</dt>
        <dd>{formatDateTime(detail.updated_at)}</dd>
        <dt>{t('memory.detail.run')}</dt>
        <dd>
          {detail.run_id ? (
            <button type="button" className="run-link" onClick={() => onOpenRun(detail.run_id as string)}>
              {t('memory.detail.viewRun')}
            </button>
          ) : (
            '—'
          )}
        </dd>
      </dl>

      <section data-testid="memory-detail-content">
        <h3>{t('memory.detail.content')}</h3>
        {content ? (
          <div className="preview">
            <pre>{content}</pre>
          </div>
        ) : (
          <p className="modal__note">{t('memory.detail.noContent')}</p>
        )}
      </section>

      {detail.content_text ? (
        <section data-testid="memory-detail-text">
          <h3>{t('memory.detail.contentText')}</h3>
          <div className="preview">
            <pre>{detail.content_text}</pre>
          </div>
        </section>
      ) : null}

      <section data-testid="memory-detail-evidence">
        <h3>
          {t('memory.detail.evidence')} ({evidence.length})
        </h3>
        {evidence.length === 0 ? (
          <p className="modal__note">{t('memory.detail.noEvidence')}</p>
        ) : (
          evidence.map((event) => (
            <article className="preview" key={event.id} data-testid={`memory-evidence-${event.id}`}>
              <header>
                <span className="badge">{event.message_type || event.source_type || '—'}</span>
                <span>{formatDateTime(event.occurred_at)}</span>
                <span>{event.id}</span>
              </header>
              <pre>{pretty(event.payload)}</pre>
            </article>
          ))
        )}
      </section>
    </>
  );
}
