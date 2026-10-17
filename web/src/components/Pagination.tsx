import { useI18n } from '../i18n';

export interface PaginationProps {
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  disabled?: boolean;
  // testId/statusTestId let several pagers coexist on one page without their
  // test ids colliding.
  testId?: string;
  statusTestId?: string;
}

// Pagination is a presentation-only pager: it derives the page count from the
// server-reported total and emits the requested page through onPageChange.
export function Pagination({ page, pageSize, total, onPageChange, disabled, testId = 'pagination', statusTestId = 'pagination-status' }: PaginationProps) {
  const { t } = useI18n();
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const canPrev = !disabled && page > 1;
  const canNext = !disabled && page < totalPages;
  return (
    <nav className="pagination" aria-label={t('pagination.aria')} data-testid={testId}>
      <button type="button" onClick={() => onPageChange(page - 1)} disabled={!canPrev}>
        {t('pagination.previous')}
      </button>
      <span data-testid={statusTestId}>
        {t('pagination.status', { page, totalPages, total })}
      </span>
      <button type="button" onClick={() => onPageChange(page + 1)} disabled={!canNext}>
        {t('pagination.next')}
      </button>
    </nav>
  );
}
