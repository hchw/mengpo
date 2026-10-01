package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

// ListFeedback returns a tenant's feedback since a point in time, newest first.
func (r *MemoryRepository) ListFeedback(ctx context.Context, tenantID string, since time.Time, limit int) ([]ports.MemoryFeedback, error) {
	if tenantID == "" || limit <= 0 {
		return nil, ErrInvalidMemory
	}
	var result []ports.MemoryFeedback
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
SELECT memory_id::text, user_id::text, COALESCE(session_id::text, ''), feedback_type, COALESCE(reason, ''), request_id, created_at
FROM memory_feedback
WHERE created_at >= $1
ORDER BY created_at DESC
LIMIT $2`, since.UTC(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ports.MemoryFeedback
			if err := rows.Scan(&item.MemoryID, &item.UserID, &item.SessionID, &item.Type, &item.Reason, &item.RequestID, &item.CreatedAt); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

var _ ports.FeedbackReader = (*MemoryRepository)(nil)
