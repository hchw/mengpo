import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { EvaluationSnapshot } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface MemoryEvaluationPageProps {
  api: ConsoleApi;
}

type MetricKey =
  | 'retrieval_precision'
  | 'promotion_precision'
  | 'wrong_memory_rate'
  | 'attribution_accuracy'
  | 'latency_ms_p95'
  | 'tokens_per_projection'
  | 'cost_usd'
  | 'cache_hit_rate';

const METRICS: Array<{ key: MetricKey; unit: 'percent' | 'ms' | 'count' | 'usd' }> = [
  { key: 'retrieval_precision', unit: 'percent' },
  { key: 'promotion_precision', unit: 'percent' },
  { key: 'wrong_memory_rate', unit: 'percent' },
  { key: 'attribution_accuracy', unit: 'percent' },
  { key: 'latency_ms_p95', unit: 'ms' },
  { key: 'tokens_per_projection', unit: 'count' },
  { key: 'cost_usd', unit: 'usd' },
  { key: 'cache_hit_rate', unit: 'percent' },
];

function format(value: number | string, unit: string): string {
  if (typeof value !== 'number') {
    return String(value);
  }
  switch (unit) {
    case 'percent':
      return `${(value * 100).toFixed(1)}%`;
    case 'ms':
      return `${value.toFixed(0)} ms`;
    case 'usd':
      return `$${value.toFixed(2)}`;
    default:
      return value.toFixed(1);
  }
}

// MemoryEvaluationPage renders the offline evaluation snapshot for the tenant.
export function MemoryEvaluationPage({ api }: MemoryEvaluationPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [snapshot, setSnapshot] = useState<EvaluationSnapshot>();

  useEffect(() => {
    let cancelled = false;
    api
      .getEvaluation()
      .then((value) => {
        if (!cancelled) {
          setSnapshot(value);
          setStatus('ready');
        }
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

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error' || !snapshot) {
    return <StateView kind="error" error={error} />;
  }
  const measured = snapshot.has_data !== false;
  const isMeasured = (key: MetricKey) => measured && (snapshot.measured_metrics ? snapshot.measured_metrics.includes(key) : true);
  return (
    <section aria-labelledby="evaluation-title" className="page page--evaluation">
      <h1 id="evaluation-title">{t('evaluation.title')}</h1>
      <p data-testid="evaluation-generated">{t('evaluation.generated', { time: new Date(snapshot.generated_at).toISOString() })}</p>
      {measured ? (
        <p data-testid="evaluation-samples">
          {t('evaluation.samples', { projections: snapshot.projections ?? 0, samples: snapshot.feedback_samples ?? 0 })}
        </p>
      ) : (
        <p data-testid="evaluation-no-data">{t('evaluation.noData')}</p>
      )}
      <dl data-testid="evaluation-metrics">
        {METRICS.map((metric) => (
          <div key={metric.key}>
            <dt>{t(`evaluation.${metric.key}`)}</dt>
            <dd data-testid={`metric-${metric.key}`}>{isMeasured(metric.key) ? format(snapshot[metric.key], metric.unit) : '—'}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
