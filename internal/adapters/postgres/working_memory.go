package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// WorkingMemoryRepository persists session-scoped working memory together with
// its evidence. It is the write path for memory that is used inside the session
// that produced it, so records arrive active and are never queued for review.
//
// The user-global background tree goes through CandidateRepository instead;
// governance owns every promotion there. This repository enforces the split by
// rejecting any non-session scope and any status other than active.
type WorkingMemoryRepository struct {
	router *tenantdb.Router
}

func NewWorkingMemoryRepository(router *tenantdb.Router) *WorkingMemoryRepository {
	return &WorkingMemoryRepository{router: router}
}

// PersistWorkingMemory writes each working-memory node and its evidence in one
// tenant transaction. It is idempotent on the idempotency keys and returns the
// number of newly inserted nodes.
func (r *WorkingMemoryRepository) PersistWorkingMemory(ctx context.Context, tenantID string, records []ports.WorkingMemoryRecord) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	inserted := 0
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		for _, record := range records {
			node := record.Node
			if err := validateMemoryWriteNode(node); err != nil {
				return err
			}
			if node.ScopeType != "session" || node.SessionID == "" {
				return fmt.Errorf("%w: working memory writer requires session scope", ErrInvalidMemory)
			}
			if node.Status != "active" {
				return fmt.Errorf("%w: working memory writer received status %q", ErrInvalidMemory, node.Status)
			}
			// Resolve the session owner inside the transaction so a writer can
			// never place working memory under another user's session.
			var ownerID string
			if err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id = $1`, node.SessionID).Scan(&ownerID); err != nil {
				return fmt.Errorf("verify working memory owner: %w", err)
			}
			if ownerID != node.UserID {
				return ErrMemoryScopeMismatch
			}
			memoryID, created, err := insertMemoryNodeTx(ctx, tx, node)
			if err != nil {
				return err
			}
			if created {
				inserted++
			}
			if err := insertMemoryEvidenceTx(ctx, tx, memoryID, record.Evidence); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

var _ ports.WorkingMemoryWriter = (*WorkingMemoryRepository)(nil)
