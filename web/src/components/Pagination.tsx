import { useI18n } from '../i18n';

export interface PaginationProps {
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  disabled?: boolean;
}

// Pagination is a presentation-only pager: it derives the page count from the
// server-reported total and emits the requested page through onPageChange.
export function Pagination({ page, pageSize, total, onPageChange, disabled }: PaginationProps) {
  const { t } = useI18n();
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const canPrev = !disabled && page > 1;
  const canNext = !disabled && page < totalPages;
  return (
    <nav className="pagination" aria-label={t('pagination.aria')} data-testid="pagination">
      <button type="button" onClick={() => onPageChange(page - 1)} disabled={!canPrev}>
        {t('pagination.previous')}
      </button>
      <span data-testid="pagination-status">
        {t('pagination.status', { page, totalPages, total })}
      </span>
      <button type="button" onClick={() => onPageChange(page + 1)} disabled={!canNext}>
        {t('pagination.next')}
      </button>
    </nav>
  );
}
