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

// ListMemoryEvidence reads the evidence rows for one memory node, joined with
// the source observed event so reliability and session come from server-side
// evidence rather than a caller claim.
func (r *CandidateRepository) ListMemoryEvidence(ctx context.Context, tenantID, memoryID string) ([]ports.MemoryEvidenceRecord, error) {
	if tenantID == "" || memoryID == "" {
		return nil, ports.ErrMemoryNotFound
	}
	var result []ports.MemoryEvidenceRecord
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
SELECT e.id::text, e.memory_id::text, e.raw_event_id::text, COALESCE(e.normalized_event_id::text, ''),
       e.evidence_role, e.confidence, e.attribution, COALESCE(o.reliability, 'unknown'), COALESCE(o.session_id::text, '')
FROM memory_evidence e
JOIN observed_events o ON o.id = e.raw_event_id
WHERE e.memory_id = $1::uuid
ORDER BY e.created_at, e.id`, memoryID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ports.MemoryEvidenceRecord
			if err := rows.Scan(&item.ID, &item.MemoryID, &item.RawEventID, &item.NormalizedEventID, &item.EvidenceRole, &item.Confidence, &item.Attribution, &item.Reliability, &item.SourceSessionID); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// InfluencedSessions returns the subset of sessionIDs whose observed events
// report having used the memory or any memory it is linked to through its
// lineage (parent chain plus generalizes relations). It never guesses: an
// unresolvable lineage simply yields no exclusions.
func (r *CandidateRepository) InfluencedSessions(ctx context.Context, tenantID, memoryID string, sessionIDs []string) (map[string]struct{}, error) {
	excluded := make(map[string]struct{})
	if tenantID == "" || memoryID == "" || len(sessionIDs) == 0 {
		return excluded, nil
	}
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
WITH RECURSIVE ancestry AS (
    SELECT id, parent_id FROM memory_nodes WHERE id = $1::uuid AND deleted_at IS NULL
    UNION
    SELECT m.id, m.parent_id FROM memory_nodes m JOIN ancestry a ON m.id = a.parent_id WHERE m.deleted_at IS NULL
),
lineage AS (
    SELECT id FROM ancestry
    UNION
    SELECT r.target_memory_id FROM memory_relations r JOIN ancestry a ON r.source_memory_id = a.id WHERE r.relation_type = 'generalizes'
    UNION
    SELECT r.source_memory_id FROM memory_relations r JOIN ancestry a ON r.target_memory_id = a.id WHERE r.relation_type = 'generalizes'
),
lineage_ids AS (SELECT COALESCE(array_agg(DISTINCT id::text), ARRAY[]::text[]) AS ids FROM lineage)
SELECT DISTINCT oe.session_id::text
FROM observed_events oe, lineage_ids
WHERE oe.session_id = ANY($2::uuid[])
  AND oe.trace_metadata IS NOT NULL
  AND oe.trace_metadata->'used_memory_ids' ?| lineage_ids.ids`, memoryID, uuidArrayLiteral(sessionIDs))
		if err != nil {
			return fmt.Errorf("query influenced sessions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var sessionID string
			if err := rows.Scan(&sessionID); err != nil {
				return err
			}
			if sessionID != "" {
				excluded[sessionID] = struct{}{}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return excluded, nil
}
