package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// CandidateRepository persists model-proposed memories as candidates together
// with the raw evidence that justifies them. It never promotes a candidate:
// governance owns every promotion.
type CandidateRepository struct {
	router *tenantdb.Router
}

func NewCandidateRepository(router *tenantdb.Router) *CandidateRepository {
	return &CandidateRepository{router: router}
}

// PersistCandidates writes each record's node and evidence in one tenant
// transaction. It is idempotent: a repeated run with the same idempotency keys
// inserts nothing new and returns 0.
func (r *CandidateRepository) PersistCandidates(ctx context.Context, tenantID string, records []ports.CandidateMemoryRecord) (int, error) {
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
			if node.Status != "candidate" {
				// Defense in depth: the writer may only create candidates.
				return fmt.Errorf("%w: candidate writer received status %q", ErrInvalidMemory, node.Status)
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
		if errors.Is(err, ports.ErrMemoryNotFound) {
			return 0, err
		}
		return 0, err
	}
	return inserted, nil
}

var _ ports.CandidateMemoryWriter = (*CandidateRepository)(nil)
