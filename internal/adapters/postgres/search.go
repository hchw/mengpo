package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidSearchPage = errors.New("invalid memory search request")

const (
	DefaultSearchPageSize = 20
	MaxSearchPageSize     = 100
	MaxSearchPage         = 100000
	MaxSearchQueryLength  = 4096
)

type SearchRepository struct {
	router *tenantdb.Router
}

func NewSearchRepository(router *tenantdb.Router) *SearchRepository {
	return &SearchRepository{router: router}
}

// Search always restricts results to the user's tenant-local User Global Memory
// and, when supplied, their current Session Memory before applying query terms.
func (r *SearchRepository) Search(ctx context.Context, tenantID, userID, sessionID string, request ports.MemorySearchRequest) (ports.MemorySearchPage, error) {
	if tenantID == "" || userID == "" || len(request.Query) > MaxSearchQueryLength || request.Page < 0 || request.Page > MaxSearchPage || request.PageSize < 0 {
		return ports.MemorySearchPage{}, ErrInvalidSearchPage
	}
	if request.Page == 0 {
		request.Page = 1
	}
	if request.PageSize == 0 {
		request.PageSize = DefaultSearchPageSize
	}
	if request.PageSize > MaxSearchPageSize {
		return ports.MemorySearchPage{}, ErrInvalidSearchPage
	}
	offset := int64(request.Page-1) * int64(request.PageSize)
	query := strings.TrimSpace(request.Query)
	var result ports.MemorySearchPage
	result.Page, result.PageSize = request.Page, request.PageSize

	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		const filters = `user_id = $1::uuid
			AND ((scope_type = 'user-global' AND scope_id = $1::uuid)
				OR (scope_type = 'session' AND session_id = NULLIF($2, '')::uuid))
AND ($2 = '' OR EXISTS (SELECT 1 FROM sessions AS requested_session WHERE requested_session.id = NULLIF($2, '')::uuid AND requested_session.user_id = $1::uuid))
			AND status IN ('active', 'stable')
			AND default_retrieval = true
			AND deleted_at IS NULL
			AND (expires_at IS NULL OR expires_at > now())
			AND ($4 = '' OR memory_type = $4)
			AND ($3 = '' OR to_tsvector('simple', coalesce(content_text, '')) @@ plainto_tsquery('simple', $3))`
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes WHERE `+filters,
			userID, sessionID, query, request.MemoryType).Scan(&result.Total); err != nil {
			return fmt.Errorf("count memory search results: %w", err)
		}
		selectQuery := `SELECT ` + memoryNodeColumns + `,
			CASE WHEN $3 = '' THEN 0::real
			ELSE ts_rank_cd(to_tsvector('simple', coalesce(content_text, '')), plainto_tsquery('simple', $3))
			END AS score
			FROM memory_nodes WHERE ` + filters + `
			ORDER BY score DESC, confidence DESC, updated_at DESC, id
			LIMIT $5 OFFSET $6`
		rows, err := tx.QueryContext(ctx, selectQuery, userID, sessionID, query, request.MemoryType, request.PageSize, offset)
		if err != nil {
			return fmt.Errorf("search memory nodes: %w", err)
		}
		defer rows.Close()
		result.Items = make([]ports.MemorySearchResult, 0, request.PageSize)
		for rows.Next() {
			var item ports.MemorySearchResult
			item.Node, item.Score, err = scanMemoryWithScore(rows)
			if err != nil {
				return fmt.Errorf("scan memory search result: %w", err)
			}
			result.Items = append(result.Items, item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate memory search results: %w", err)
		}
		return nil
	})
	if err != nil {
		return ports.MemorySearchPage{}, err
	}
	return result, nil
}

func scanMemoryWithScore(row rowScanner) (ports.MemoryNodeRecord, float64, error) {
	var node ports.MemoryNodeRecord
	var sessionID, parentID sql.NullString
	var expiresAt, deletedAt sql.NullTime
	var applicability, content, provenance []byte
	var score float64
	err := row.Scan(
		&node.ID, &node.IdempotencyKey, &node.UserID, &sessionID, &node.ScopeType, &node.ScopeID,
		&parentID, &node.MemoryType, &node.Status, &node.Visibility, &node.Confidence,
		&applicability, &content, &node.ContentText, &node.DefaultRetrieval, &node.Version,
		&node.CreatedAt, &node.UpdatedAt, &provenance, &expiresAt, &deletedAt, &score,
	)
	if err != nil {
		return ports.MemoryNodeRecord{}, 0, err
	}
	if sessionID.Valid {
		node.SessionID = sessionID.String
	}
	if parentID.Valid {
		node.ParentID = parentID.String
	}
	node.Applicability = append(node.Applicability[:0], applicability...)
	node.Content = append(node.Content[:0], content...)
	node.Provenance = append(node.Provenance[:0], provenance...)
	if expiresAt.Valid {
		node.ExpiresAt = &expiresAt.Time
	}
	if deletedAt.Valid {
		node.DeletedAt = &deletedAt.Time
	}
	return node, score, nil
}
