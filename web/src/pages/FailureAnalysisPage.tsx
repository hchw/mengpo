import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { FailureRecord } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface FailureAnalysisPageProps {
  api: ConsoleApi;
}

const CONFIDENCE_ORDER: FailureRecord['confidence'][] = ['confirmed', 'inferred', 'suspected', 'unknown'];
const ATTRIBUTION_ORDER: FailureRecord['attribution'][] = ['direct', 'correlated', 'inferred', 'unknown'];

// FailureAnalysisPage keeps failure confidence and attribution completeness as
// separate, explicit dimensions.
export function FailureAnalysisPage({ api }: FailureAnalysisPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [failures, setFailures] = useState<FailureRecord[]>([]);
  const [confidence, setConfidence] = useState<'all' | FailureRecord['confidence']>('all');

  const load = useCallback(() => {
    setStatus('loading');
    api
      .listFailures()
      .then((page) => {
        setFailures(page.items);
        setStatus(page.items.length === 0 ? 'empty' : 'ready');
      })
      .catch((cause: unknown) => {
        setError(cause);
        setStatus('error');
      });
  }, [api]);

  useEffect(() => {
    load();
  }, [load]);

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description={t('failures.empty')} />;
  }
  const visible = confidence === 'all' ? failures : failures.filter((failure) => failure.confidence === confidence);
  return (
    <section aria-labelledby="failure-title" className="page page--failure">
      <h1 id="failure-title">{t('failures.title')}</h1>
      <div role="group" aria-label={t('failures.filterAria')}>
        {(['all', ...CONFIDENCE_ORDER] as const).map((value) => (
          <button key={value} aria-pressed={confidence === value} onClick={() => setConfidence(value)}>
            {t(`failures.confidence.${value}`)}
          </button>
        ))}
      </div>
      <ul data-testid="failure-list">
        {visible.map((failure) => (
          <li key={failure.id} data-testid={`failure-${failure.id}`}>
            <span>{failure.title}</span>
            <span data-testid={`failure-confidence-${failure.id}`}>{failure.confidence}</span>
            <span data-testid={`failure-attribution-${failure.id}`}>{failure.attribution}</span>
          </li>
        ))}
      </ul>
      <details>
        <summary>{t('failures.attributionCompleteness')}</summary>
        <ul>
          {ATTRIBUTION_ORDER.map((value) => (
            <li key={value} data-testid={`attribution-count-${value}`}>
              {t(`failures.attribution.${value}`)}: {failures.filter((failure) => failure.attribution === value).length}
            </li>
          ))}
        </ul>
      </details>
    </section>
  );
}
