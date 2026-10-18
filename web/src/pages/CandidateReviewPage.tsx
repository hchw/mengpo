import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import { ApiClientError } from '../api/client';
import type { CandidateRecord } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';
import { DuplicateGroupDialog } from './DuplicateGroupDialog';
import { MergeCandidateDialog } from './MergeCandidateDialog';

export type ReviewAction = 'confirm' | 'reject' | 'correct' | 'merge' | 'expire' | 'delete';

export interface CandidateReviewPageProps {
  api: ConsoleApi;
}

interface ReviewOutcome {
  candidateId: string;
  action: ReviewAction;
}

const ACTIONS: ReviewAction[] = ['confirm', 'reject', 'correct', 'merge', 'expire', 'delete'];

// CandidateReviewPage applies governance decisions with optimistic updates.
// A conflicted decision (HTTP 409) rolls the candidate list back and surfaces a
// conflict state instead of leaving the UI out of sync.
export function CandidateReviewPage({ api }: CandidateReviewPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [candidates, setCandidates] = useState<CandidateRecord[]>([]);
  const [rollback, setRollback] = useState<ReviewOutcome>();
  const [conflict, setConflict] = useState<string>();
  const [duplicateGroup, setDuplicateGroup] = useState<string>();
  const [mergeTarget, setMergeTarget] = useState<CandidateRecord>();

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
    return <StateView kind="empty" description={t('review.empty')} />;
  }
  return (
    <section aria-labelledby="review-title" className="page page--review">
      <h1 id="review-title">{t('review.title')}</h1>
      {conflict ? <StateView kind="conflict" description={t('review.conflict')} /> : null}
      {rollback && !conflict ? (
        <StateView
          kind="rollback"
          description={t('review.rollback', { action: t(`review.action.${rollback.action}`), id: rollback.candidateId })}
        />
      ) : null}
      <ul data-testid="candidate-list">
        {candidates.map((candidate) => (
          <li key={candidate.id} data-testid={`candidate-${candidate.id}`}>
            <span>{candidate.content_summary}</span>
            <span data-testid={`candidate-confidence-${candidate.id}`}>{candidate.confidence.toFixed(2)}</span>
            {candidate.proposed_by ? (
              <span data-testid={`candidate-proposed-by-${candidate.id}`}>
                {t('review.proposedBy')}: {candidate.proposed_by}
              </span>
            ) : null}
            {candidate.evidence_ids && candidate.evidence_ids.length > 0 ? (
              <span data-testid={`candidate-evidence-${candidate.id}`}>
                {t('review.evidence')}: {candidate.evidence_ids.length}
              </span>
            ) : null}
            {candidate.triage_group ? (
              <button
                type="button"
                className="run-link"
                data-testid={`candidate-duplicate-${candidate.id}`}
                onClick={() => setDuplicateGroup(candidate.triage_group)}
              >
                {t('review.duplicate')}: {candidate.triage_group}
              </button>
            ) : null}
            {candidate.triage_rationale ? (
              <span data-testid={`candidate-triage-${candidate.id}`}>
                {t('review.triage')}: {candidate.triage_rationale}
              </span>
            ) : null}
            {candidate.triage_conflicts && candidate.triage_conflicts.length > 0 ? (
              <span data-testid={`candidate-conflicts-${candidate.id}`}>
                {t('review.conflicts')}: {candidate.triage_conflicts.length}
              </span>
            ) : null}
            {ACTIONS.map((action) => (
              <button key={action} onClick={() => (action === 'merge' ? setMergeTarget(candidate) : void decide(candidate, action))}>
                {t(`review.action.${action}`)}
              </button>
            ))}
          </li>
        ))}
      </ul>
      {duplicateGroup ? <DuplicateGroupDialog api={api} groupId={duplicateGroup} onClose={() => setDuplicateGroup(undefined)} /> : null}
      {mergeTarget ? (
        <MergeCandidateDialog
          api={api}
          candidate={mergeTarget}
          peers={candidates.filter((item) => item.id !== mergeTarget.id)}
          onClose={() => setMergeTarget(undefined)}
          onMerged={() => {
            setMergeTarget(undefined);
            load();
          }}
        />
      ) : null}
    </section>
  );
}
