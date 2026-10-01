import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import { ApiClientError } from '../api/client';
import type { CandidateRecord } from '../api/types';
import { StateView } from '../components/states';

export type ReviewAction = 'confirm' | 'reject' | 'correct' | 'merge' | 'expire' | 'delete';

export interface CandidateReviewPageProps {
  api: ConsoleApi;
}

interface ReviewOutcome {
  candidateId: string;
  action: ReviewAction;
}

// CandidateReviewPage applies governance decisions with optimistic updates.
// A conflicted decision (HTTP 409) rolls the candidate list back and surfaces a
// conflict state instead of leaving the UI out of sync.
export function CandidateReviewPage({ api }: CandidateReviewPageProps) {
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [candidates, setCandidates] = useState<CandidateRecord[]>([]);
  const [rollback, setRollback] = useState<ReviewOutcome>();
  const [conflict, setConflict] = useState<string>();

  const load = useCallback(() => {
    setStatus('loading');
    api
      .listCandidates()
      .then((page) => {
        setCandidates(page.items);
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

  const decide = useCallback(
    async (candidate: CandidateRecord, action: ReviewAction) => {
      const snapshot = candidates;
      setRollback(undefined);
      setConflict(undefined);
      setCandidates((current) => current.filter((item) => item.id !== candidate.id));
      try {
        await api.reviewCandidate(candidate.id, action, action === 'correct' ? { content_summary: candidate.content_summary } : {});
      } catch (cause) {
        setCandidates(snapshot);
        if (cause instanceof ApiClientError && cause.code === 'CONFLICT') {
          setConflict(candidate.id);
        } else {
          setError(cause);
        }
        setRollback({ candidateId: candidate.id, action });
      }
    },
    [api, candidates],
  );

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description="No candidate memories awaiting review." />;
  }
  return (
    <section aria-labelledby="review-title" className="page page--review">
      <h1 id="review-title">Candidate Review</h1>
      {conflict ? <StateView kind="conflict" description="This candidate changed while you were reviewing it." /> : null}
      {rollback && !conflict ? <StateView kind="rollback" description={`Could not ${rollback.action} candidate ${rollback.candidateId}.`} /> : null}
      <ul data-testid="candidate-list">
        {candidates.map((candidate) => (
          <li key={candidate.id} data-testid={`candidate-${candidate.id}`}>
            <span>{candidate.content_summary}</span>
            <span data-testid={`candidate-confidence-${candidate.id}`}>{candidate.confidence.toFixed(2)}</span>
            {(['confirm', 'reject', 'correct', 'merge', 'expire', 'delete'] as ReviewAction[]).map((action) => (
              <button key={action} onClick={() => void decide(candidate, action)}>
                {action}
              </button>
            ))}
          </li>
        ))}
      </ul>
    </section>
  );
}