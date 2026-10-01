package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
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
			if node.ID == "" || node.IdempotencyKey == "" || node.UserID == "" || node.ScopeID == "" ||
				node.ScopeType == "" || !json.Valid(node.Content) {
				return ErrInvalidMemory
			}
			if node.Status != "candidate" {
				// Defense in depth: the writer may only create candidates.
				return fmt.Errorf("%w: candidate writer received status %q", ErrInvalidMemory, node.Status)
			}
			if node.Visibility == "" {
				node.Visibility = "private"
			}
			if len(node.Applicability) == 0 {
				node.Applicability = json.RawMessage(`{}`)
			}
			if len(node.Provenance) == 0 {
				node.Provenance = json.RawMessage(`{}`)
			}
			result, err := tx.ExecContext(ctx, `
INSERT INTO memory_nodes (
	id, idempotency_key, user_id, session_id, scope_type, scope_id, parent_id,
	memory_type, status, visibility, confidence, applicability, content,
	content_text, default_retrieval, version, created_at, updated_at, provenance
) VALUES (
	$1, $2, $3, $4, $5, $6, NULL, $7, $8, $9, $10, $11::jsonb, $12::jsonb,
	$13, $14, 1, now(), now(), $15::jsonb
) ON CONFLICT (idempotency_key) DO NOTHING`,
				node.ID, node.IdempotencyKey, node.UserID, nullable(node.SessionID), node.ScopeType,
				node.ScopeID, node.MemoryType, node.Status, node.Visibility, node.Confidence,
				string(node.Applicability), string(node.Content), node.ContentText, node.DefaultRetrieval,
				string(node.Provenance))
			if err != nil {
				return fmt.Errorf("insert candidate node: %w", err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("check candidate insert: %w", err)
			}
			memoryID := node.ID
			if rows == 0 {
				if err := tx.QueryRowContext(ctx, `SELECT id FROM memory_nodes WHERE idempotency_key = $1`, node.IdempotencyKey).Scan(&memoryID); err != nil {
					return fmt.Errorf("resolve existing candidate: %w", err)
				}
			} else {
				inserted++
			}
			for _, evidence := range record.Evidence {
				if evidence.RawEventID == "" || evidence.EvidenceRole == "" {
					return fmt.Errorf("%w: evidence requires raw event and role", ErrInvalidMemory)
				}
				attribution := evidence.Attribution
				if attribution == "" {
					attribution = "inferred"
				}
				if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_evidence (id, memory_id, raw_event_id, normalized_event_id, evidence_role, confidence, attribution)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
ON CONFLICT (memory_id, raw_event_id, evidence_role) DO NOTHING`,
					memoryID, evidence.RawEventID, nullable(evidence.NormalizedEventID), evidence.EvidenceRole,
					evidence.Confidence, attribution); err != nil {
					return fmt.Errorf("insert candidate evidence: %w", err)
				}
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
