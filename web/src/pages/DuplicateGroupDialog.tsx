import { useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import type { DuplicateGroupMember } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface DuplicateGroupDialogProps {
  api: ConsoleApi;
  groupId: string;
  onClose: () => void;
}

// DuplicateGroupDialog turns an opaque duplicate-group id into the memories it
// actually contains, so a reviewer can tell whether the advisory near-duplicate
// finding is a real duplicate before acting on it.
export function DuplicateGroupDialog({ api, groupId, onClose }: DuplicateGroupDialogProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [members, setMembers] = useState<DuplicateGroupMember[]>([]);

  useEffect(() => {
    let cancelled = false;
    setStatus('loading');
    api
      .listDuplicateGroup(groupId)
      .then((page) => {
        if (cancelled) return;
        setMembers(page.items ?? []);
        setStatus('ready');
      })
      .catch((cause) => {
        if (cancelled) return;
        setError(cause);
        setStatus('error');
      });
    return () => {
      cancelled = true;
    };
  }, [api, groupId]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div className="modal" role="dialog" aria-modal="true" aria-labelledby="duplicate-group-title" onClick={onClose}>
      <div className="modal__panel" onClick={(event) => event.stopPropagation()}>
        <header className="modal__header">
          <h2 id="duplicate-group-title" className="modal__title">
            {t('review.duplicateTitle')}
          </h2>
          <button type="button" className="modal__close" aria-label={t('review.duplicateClose')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal__body" data-testid="duplicate-group-dialog">
          <p className="modal__note">{t('review.duplicateHint')}</p>
          <code className="modal__note" data-testid="duplicate-group-id">
            {groupId}
          </code>
          {status === 'loading' ? <StateView kind="loading" /> : null}
          {status === 'error' ? <StateView kind="error" error={error} /> : null}
          {status === 'ready' ? (
            members.length === 0 ? (
              <p className="modal__note" data-testid="duplicate-group-empty">
                {t('review.duplicateEmpty')}
              </p>
            ) : (
              <ul className="duplicate-list">
                {members.map((member) => (
                  <li key={member.id} className="duplicate-member" data-testid={`duplicate-group-member-${member.id}`}>
                    <header>
                      <span>{member.status}</span>
                      <span>{member.confidence.toFixed(2)}</span>
                      <span>{member.scope_type}</span>
                    </header>
                    <p>{member.content || '—'}</p>
                  </li>
                ))}
              </ul>
            )
          ) : null}
        </div>
      </div>
    </div>
  );
}
