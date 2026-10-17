import type { MemoryRecord } from '../api/types';
import { formatDateTime } from '../format';
import { useI18n } from '../i18n';

export interface MemoryCardProps {
  memory: MemoryRecord;
  onOpen: (id: string) => void;
  // stale marks a memory that has not been reinforced recently; it is surfaced
  // as a badge in the meta row.
  stale?: boolean;
}

// MemoryCard is the shared row used by the session and background memory lists.
// The content sits on top, the meta badges below it, and the updated time is
// pinned to the bottom-right with the detail action opposite, so long text can
// never crowd the timestamp.
export function MemoryCard({ memory, onOpen, stale }: MemoryCardProps) {
  const { t } = useI18n();
  return (
    <li className="memory-card" data-testid={`memory-${memory.id}`}>
      <div className="memory-card__main">
        <p className="memory-card__text">{memory.content_summary}</p>
        <div className="memory-card__meta">
          <span className="badge">{memory.type}</span>
          <span data-testid={`memory-status-${memory.id}`}>{memory.status}</span>
          <span className="badge">{t('memory.confidence', { value: memory.confidence.toFixed(2) })}</span>
          {stale ? <span data-testid={`memory-stale-${memory.id}`}>{t('background.stale')}</span> : null}
        </div>
      </div>
      <div className="memory-card__foot">
        <button type="button" className="btn-ghost memory-card__detail" onClick={() => onOpen(memory.id)}>
          {t('memory.viewDetail')}
        </button>
        <span className="memory-card__time" data-testid={`memory-updated-${memory.id}`}>
          {t('memory.updated', { time: formatDateTime(memory.updated_at) })}
        </span>
      </div>
    </li>
  );
}
