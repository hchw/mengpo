import { useEffect, useMemo, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { CandidateRecord } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface MergeCandidateDialogProps {
  api: ConsoleApi;
  /** The candidate the selected memories are merged into. */
  candidate: CandidateRecord;
  /** Other candidates on the current page, offered as merge sources. */
  peers: CandidateRecord[];
  onClose: () => void;
  onMerged: () => void;
}

interface MergeOption {
  id: string;
  content: string;
  confidence?: number;
}

// MergeCandidateDialog collects the candidates to merge into the target. The
// merge API needs an explicit target and at least one duplicate, which the old
// one-click action never sent; a group's members are pre-selected so the common
// "merge this suspected duplicate group" case is one confirmation.
export function MergeCandidateDialog({ api, candidate, peers, onClose, onMerged }: MergeCandidateDialogProps) {
  const { t } = useI18n();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [groupMembers, setGroupMembers] = useState<MergeOption[]>([]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();

  useEffect(() => {
    let cancelled = false;
    if (!candidate.triage_group) {
      return () => {
        cancelled = true;
      };
    }
    api
      .listDuplicateGroup(candidate.triage_group)
      .then((page) => {
        if (cancelled) return;
        const members = (page.items ?? []).filter((member) => member.id !== candidate.id).map((member) => ({ id: member.id, content: member.content, confidence: member.confidence }));
        setGroupMembers(members);
        setSelected(new Set(members.map((member) => member.id)));
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause);
      });
    return () => {
      cancelled = true;
    };
  }, [api, candidate.id, candidate.triage_group]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const options = useMemo<MergeOption[]>(() => {
    const byId = new Map<string, MergeOption>();
    for (const member of groupMembers) {
      byId.set(member.id, member);
    }
    for (const peer of peers) {
      if (peer.id === candidate.id || byId.has(peer.id)) continue;
      byId.set(peer.id, { id: peer.id, content: peer.content_summary, confidence: peer.confidence });
    }
    return [...byId.values()];
  }, [groupMembers, peers, candidate.id]);

  const toggle = (id: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const submit = async () => {
    setPending(true);
    setError(undefined);
    try {
      await api.reviewCandidate(candidate.id, 'merge', { target_memory_id: candidate.id, duplicate_memory_ids: [...selected] });
      onMerged();
    } catch (cause) {
      setError(cause);
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="modal" role="dialog" aria-modal="true" aria-labelledby="merge-candidate-title" onClick={onClose}>
      <div className="modal__panel" onClick={(event) => event.stopPropagation()}>
        <header className="modal__header">
          <h2 id="merge-candidate-title" className="modal__title">
            {t('review.mergeTitle')}
          </h2>
          <button type="button" className="modal__close" aria-label={t('review.mergeClose')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal__body" data-testid="merge-candidate-dialog">
          <p className="modal__note">{t('review.mergeHint', { id: candidate.id })}</p>
          <p className="duplicate-member p" data-testid="merge-target">
            {candidate.content_summary}
          </p>
          {error ? <StateView kind="error" error={error} /> : null}
          {options.length === 0 ? (
            <p className="modal__note" data-testid="merge-empty">
              {t('review.mergeEmpty')}
            </p>
          ) : (
            <ul className="duplicate-list">
              {options.map((option) => (
                <li key={option.id} className="duplicate-member">
                  <label className="merge-option">
                    <input
                      type="checkbox"
                      checked={selected.has(option.id)}
                      data-testid={`merge-option-${option.id}`}
                      onChange={() => toggle(option.id)}
                    />
                    <span>{option.content}</span>
                    {option.confidence !== undefined ? <span data-testid={`merge-option-confidence-${option.id}`}>{option.confidence.toFixed(2)}</span> : null}
                  </label>
                </li>
              ))}
            </ul>
          )}
          <div className="trigger-actions">
            <button type="button" className="btn btn-primary" data-testid="merge-submit" disabled={pending || selected.size === 0} onClick={() => void submit()}>
              {t('review.mergeSubmit')}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
