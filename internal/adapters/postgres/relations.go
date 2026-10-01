package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

const MaxRelatedMemories = 200

// RelationRepository expands seed memories through memory_relations. It only
// ever reads the tenant schema selected by the router; relations carry
// evidence, they never change the scope boundary.
type RelationRepository struct {
	router *tenantdb.Router
}

func NewRelationRepository(router *tenantdb.Router) *RelationRepository {
	return &RelationRepository{router: router}
}

func (r *RelationRepository) ListRelated(ctx context.Context, tenantID string, memoryIDs []string, limit int) ([]ports.RelatedMemory, error) {
	if tenantID == "" || len(memoryIDs) == 0 {
		return nil, nil
	}
	if limit < 1 || limit > MaxRelatedMemories {
		limit = MaxRelatedMemories
	}
	placeholders := make([]string, len(memoryIDs))
	args := make([]any, 0, len(memoryIDs)+1)
	for index, id := range memoryIDs {
		if id == "" {
			return nil, fmt.Errorf("empty seed memory id at index %d", index)
		}
		placeholders[index] = fmt.Sprintf("$%d::uuid", index+2)
		args = append(args, id)
	}
	seedSet := strings.Join(placeholders, ", ")
	results := make([]ports.RelatedMemory, 0, limit)
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			WITH seeds AS (SELECT unnest(ARRAY[`+seedSet+`]::uuid[]) AS seed_id),
			ranked AS (
				SELECT DISTINCT ON (other.id)
					other.*, relation.relation_type, relation.confidence AS relation_confidence
				FROM seeds
				JOIN memory_relations AS relation
					ON relation.source_memory_id = seeds.seed_id OR relation.target_memory_id = seeds.seed_id
				JOIN memory_nodes AS other
					ON other.id = CASE WHEN relation.source_memory_id = seeds.seed_id THEN relation.target_memory_id ELSE relation.source_memory_id END
				WHERE other.deleted_at IS NULL
				  AND other.status IN ('active', 'stable', 'candidate')
				ORDER BY other.id, relation.confidence DESC
			)
			SELECT `+memoryNodeColumns+`, relation_type, relation_confidence
			FROM ranked AS other
			ORDER BY relation_confidence DESC, updated_at DESC, id
			LIMIT $1`, append([]any{limit}, args...)...)
		if err != nil {
			return fmt.Errorf("query related memories: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanRelatedMemory(rows)
			if err != nil {
				return fmt.Errorf("scan related memory: %w", err)
			}
			results = append(results, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func scanRelatedMemory(row rowScanner) (ports.RelatedMemory, error) {
	var item ports.RelatedMemory
	var sessionID, parentID sql.NullString
	var expiresAt, deletedAt sql.NullTime
	var applicability, content, provenance []byte
	err := row.Scan(
		&item.Node.ID, &item.Node.IdempotencyKey, &item.Node.UserID, &sessionID, &item.Node.ScopeType, &item.Node.ScopeID,
		&parentID, &item.Node.MemoryType, &item.Node.Status, &item.Node.Visibility, &item.Node.Confidence,
		&applicability, &content, &item.Node.ContentText, &item.Node.DefaultRetrieval, &item.Node.Version,
		&item.Node.CreatedAt, &item.Node.UpdatedAt, &provenance, &expiresAt, &deletedAt,
		&item.RelationType, &item.Confidence,
	)
	if err != nil {
		return ports.RelatedMemory{}, err
	}
	if sessionID.Valid {
		item.Node.SessionID = sessionID.String
	}
	if parentID.Valid {
		item.Node.ParentID = parentID.String
	}
	item.Node.Applicability = append(item.Node.Applicability[:0], applicability...)
	item.Node.Content = append(item.Node.Content[:0], content...)
	item.Node.Provenance = append(item.Node.Provenance[:0], provenance...)
	if expiresAt.Valid {
		item.Node.ExpiresAt = &expiresAt.Time
	}
	if deletedAt.Valid {
		item.Node.DeletedAt = &deletedAt.Time
	}
	return item, nil
}
