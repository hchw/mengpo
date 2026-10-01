import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { EvaluationSnapshot } from '../api/types';
import { StateView } from '../components/states';

export interface MemoryEvaluationPageProps {
  api: ConsoleApi;
}

const METRICS: Array<{ key: keyof EvaluationSnapshot; label: string; unit: 'percent' | 'ms' | 'count' | 'usd' }> = [
  { key: 'retrieval_precision', label: 'Retrieval precision', unit: 'percent' },
  { key: 'promotion_precision', label: 'Promotion precision', unit: 'percent' },
  { key: 'wrong_memory_rate', label: 'Wrong memory rate', unit: 'percent' },
  { key: 'attribution_accuracy', label: 'Attribution accuracy', unit: 'percent' },
  { key: 'latency_ms_p95', label: 'Latency p95', unit: 'ms' },
  { key: 'tokens_per_projection', label: 'Tokens / projection', unit: 'count' },
  { key: 'cost_usd', label: 'Cost', unit: 'usd' },
  { key: 'cache_hit_rate', label: 'Cache hit rate', unit: 'percent' },
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
  return (
    <section aria-labelledby="evaluation-title" className="page page--evaluation">
      <h1 id="evaluation-title">Memory Evaluation</h1>
      <p data-testid="evaluation-generated">Generated {new Date(snapshot.generated_at).toISOString()}</p>
      <dl data-testid="evaluation-metrics">
        {METRICS.map((metric) => (
          <div key={metric.key}>
            <dt>{metric.label}</dt>
            <dd data-testid={`metric-${metric.key}`}>{format(snapshot[metric.key], metric.unit)}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}