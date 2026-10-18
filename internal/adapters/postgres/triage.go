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

var ErrInvalidTriageRecord = errors.New("invalid triage record")

// TriageRepository persists advisory triage artifacts and candidate lineage.
// Neither operation changes a candidate's state; promotion stays with governance.
type TriageRepository struct{ router *tenantdb.Router }

func NewTriageRepository(router *tenantdb.Router) *TriageRepository {
	return &TriageRepository{router: router}
}

// PersistCandidateTriage appends triage findings in one tenant transaction.
func (r *TriageRepository) PersistCandidateTriage(ctx context.Context, tenantID string, records []ports.CandidateTriageRecord) error {
	if tenantID == "" || len(records) == 0 {
		return nil
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		for _, record := range records {
			if record.ID == "" || record.MemoryID == "" || record.RunID == "" {
				return ErrInvalidTriageRecord
			}
			conflictIDs := record.ConflictIDs
			if conflictIDs == nil {
				conflictIDs = []string{}
			}
			encoded, err := json.Marshal(conflictIDs)
			if err != nil {
				return fmt.Errorf("encode triage conflicts: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO candidate_triage (id, memory_id, run_id, duplicate_group, conflict_ids, priority, rationale, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6, $7, $8)`,
				record.ID, record.MemoryID, record.RunID, record.DuplicateGroup, string(encoded), record.Priority, record.Rationale, record.CreatedAt); err != nil {
				return fmt.Errorf("persist candidate triage: %w", err)
			}
		}
		return nil
	})
}

// AddMemoryRelations inserts lineage edges, ignoring duplicates. It never
// mutates the connected memory nodes.
func (r *TriageRepository) AddMemoryRelations(ctx context.Context, tenantID string, relations []ports.MemoryRelationRecord) error {
	if tenantID == "" || len(relations) == 0 {
		return nil
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		for _, relation := range relations {
			if relation.ID == "" || relation.SourceMemoryID == "" || relation.TargetMemoryID == "" || relation.RelationType == "" {
				return ErrInvalidTriageRecord
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_relations (id, source_memory_id, target_memory_id, relation_type, confidence, created_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
ON CONFLICT (source_memory_id, target_memory_id, relation_type) DO NOTHING`,
				relation.ID, relation.SourceMemoryID, relation.TargetMemoryID, relation.RelationType, relation.Confidence, relation.CreatedAt); err != nil {
				return fmt.Errorf("add memory relation: %w", err)
			}
		}
		return nil
	})
}

var (
	_ ports.CandidateTriageWriter = (*TriageRepository)(nil)
	_ ports.CandidateTriageReader = (*TriageRepository)(nil)
	_ ports.MemoryRelationWriter  = (*TriageRepository)(nil)
)

// LatestCandidateTriage returns the most recent triage finding for each of the
// given memories. Memories without a finding are simply absent from the map.
func (r *TriageRepository) LatestCandidateTriage(ctx context.Context, tenantID string, memoryIDs []string) (map[string]ports.CandidateTriageRecord, error) {
	result := make(map[string]ports.CandidateTriageRecord)
	if tenantID == "" || len(memoryIDs) == 0 {
		return result, nil
	}
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT ON (memory_id) id::text, memory_id::text, run_id, duplicate_group, conflict_ids, priority, rationale, created_at
FROM candidate_triage
WHERE memory_id = ANY($1::uuid[])
ORDER BY memory_id, created_at DESC, id DESC`, uuidArrayLiteral(memoryIDs))
		if err != nil {
			return fmt.Errorf("query candidate triage: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var record ports.CandidateTriageRecord
			var conflicts []byte
			if err := rows.Scan(&record.ID, &record.MemoryID, &record.RunID, &record.DuplicateGroup, &conflicts, &record.Priority, &record.Rationale, &record.CreatedAt); err != nil {
				return err
			}
			if len(conflicts) > 0 {
				_ = json.Unmarshal(conflicts, &record.ConflictIDs)
			}
			result[record.MemoryID] = record
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DuplicateGroupMembers returns the memories whose latest triage finding shares
// the given duplicate group. Group ids are opaque (a node id or a content
// digest), so the lookup is a text equality against the latest finding per
// memory; older findings for the same memory are ignored.
func (r *TriageRepository) DuplicateGroupMembers(ctx context.Context, tenantID, groupID string) ([]ports.DuplicateGroupMember, error) {
	if tenantID == "" || groupID == "" {
		return nil, nil
	}
	var result []ports.DuplicateGroupMember
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
WITH latest AS (
	SELECT DISTINCT ON (memory_id) memory_id, duplicate_group
	FROM candidate_triage
	ORDER BY memory_id, created_at DESC, id DESC
)
SELECT m.id::text, m.status, m.scope_type, m.scope_id::text, m.confidence, COALESCE(m.content_text, ''), m.created_at
FROM latest
JOIN memory_nodes m ON m.id = latest.memory_id
WHERE latest.duplicate_group = $1
ORDER BY m.created_at, m.id`, groupID)
		if err != nil {
			return fmt.Errorf("query duplicate group: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var member ports.DuplicateGroupMember
			if err := rows.Scan(&member.MemoryID, &member.Status, &member.ScopeType, &member.ScopeID, &member.Confidence, &member.Content, &member.CreatedAt); err != nil {
				return err
			}
			result = append(result, member)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
