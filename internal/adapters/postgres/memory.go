package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

var (
	ErrMemoryNotFound      = ports.ErrMemoryNotFound
	ErrVersionConflict     = errors.New("memory node version conflict")
	ErrInvalidMemory       = errors.New("invalid memory node")
	ErrMemoryScopeMismatch = errors.New("memory scope does not match owner")
	ErrInvalidScopeTree    = errors.New("invalid memory scope tree query")
)

const (
	MaxParentDepth = 16
	MaxTreeNodes   = 1000
)

const memoryNodeColumns = `id, idempotency_key, user_id, session_id, scope_type, scope_id,
parent_id, memory_type, status, visibility, confidence, applicability, content,
content_text, default_retrieval, version, created_at, updated_at, provenance, expires_at, deleted_at`

type MemoryRepository struct {
	router *tenantdb.Router
}

func NewMemoryRepository(router *tenantdb.Router) *MemoryRepository {
	return &MemoryRepository{router: router}
}

func (r *MemoryRepository) Create(ctx context.Context, tenantID string, node ports.MemoryNodeRecord) (ports.MemoryNodeRecord, error) {
	if node.ID == "" || node.IdempotencyKey == "" || node.UserID == "" || node.ScopeID == "" ||
		node.ScopeType == "" || node.MemoryType == "" || node.Status == "" || !json.Valid(node.Content) {
		return ports.MemoryNodeRecord{}, ErrInvalidMemory
	}
	if !validMemoryScope(node) {
		return ports.MemoryNodeRecord{}, ErrInvalidMemory
	}
	if len(node.Applicability) == 0 {
		node.Applicability = json.RawMessage(`{}`)
	}
	if !json.Valid(node.Applicability) {
		return ports.MemoryNodeRecord{}, ErrInvalidMemory
	}
	if len(node.Provenance) == 0 {
		node.Provenance = json.RawMessage(`{}`)
	}
	if !json.Valid(node.Provenance) {
		return ports.MemoryNodeRecord{}, ErrInvalidMemory
	}
	if node.Visibility == "" {
		node.Visibility = "private"
	}
	node.Version = 1
	now := time.Now().UTC()
	var result ports.MemoryNodeRecord
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if node.ScopeType == "session" {
			var ownerID string
			err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id = $1`, node.SessionID).Scan(&ownerID)
			if errors.Is(err, sql.ErrNoRows) || err == nil && ownerID != node.UserID {
				return ErrMemoryScopeMismatch
			}
			if err != nil {
				return fmt.Errorf("verify session memory owner: %w", err)
			}
		}
		inserted, err := tx.ExecContext(ctx, `
			INSERT INTO memory_nodes (
				id, idempotency_key, user_id, session_id, scope_type, scope_id, parent_id,
				memory_type, status, visibility, confidence, applicability, content,
				content_text, default_retrieval, version, created_at, updated_at, provenance
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb,
				$14, $15, 1, $16, $16, $17::jsonb
			) ON CONFLICT (idempotency_key) DO NOTHING`,
			node.ID, node.IdempotencyKey, node.UserID, nullable(node.SessionID), node.ScopeType,
			node.ScopeID, nullable(node.ParentID), node.MemoryType, node.Status, node.Visibility,
			node.Confidence, string(node.Applicability), string(node.Content), node.ContentText,
			node.DefaultRetrieval, now, string(node.Provenance))
		if err != nil {
			return fmt.Errorf("insert memory node: %w", err)
		}
		rows, err := inserted.RowsAffected()
		if err != nil {
			return fmt.Errorf("check memory insert: %w", err)
		}
		if rows == 0 {
			result, err = getByIdempotencyKey(ctx, tx, node.IdempotencyKey)
			if err != nil {
				return err
			}
			if result.UserID != node.UserID || result.ScopeType != node.ScopeType || result.ScopeID != node.ScopeID {
				return ErrMemoryScopeMismatch
			}
			return nil
		}
		result, err = getMemory(ctx, tx, node.ID)
		return err
	})
	if err != nil {
		return ports.MemoryNodeRecord{}, err
	}
	return result, nil
}

func (r *MemoryRepository) Get(ctx context.Context, tenantID, nodeID string) (ports.MemoryNodeRecord, error) {
	var result ports.MemoryNodeRecord
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var err error
		result, err = getMemory(ctx, tx, nodeID)
		return err
	})
	return result, err
}

func (r *MemoryRepository) Update(ctx context.Context, tenantID string, node ports.MemoryNodeRecord, expectedVersion int64) (ports.MemoryNodeRecord, error) {
	if node.ID == "" || expectedVersion <= 0 || !json.Valid(node.Content) {
		return ports.MemoryNodeRecord{}, ErrInvalidMemory
	}
	if len(node.Applicability) == 0 {
		node.Applicability = json.RawMessage(`{}`)
	}
	if !json.Valid(node.Applicability) {
		return ports.MemoryNodeRecord{}, ErrInvalidMemory
	}
	if node.Visibility == "" {
		node.Visibility = "private"
	}

	var result ports.MemoryNodeRecord
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			UPDATE memory_nodes
			SET status = $1, visibility = $2, confidence = $3, applicability = $4::jsonb,
				content = $5::jsonb, content_text = $6, default_retrieval = $7,
				version = version + 1, updated_at = now()
			WHERE id = $8 AND version = $9
			RETURNING `+memoryNodeColumns,
			node.Status, node.Visibility, node.Confidence, string(node.Applicability), string(node.Content),
			node.ContentText, node.DefaultRetrieval, node.ID, expectedVersion)
		updated, err := scanMemory(row)
		if err == nil {
			result = updated
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("update memory node: %w", err)
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM memory_nodes WHERE id = $1)`, node.ID).Scan(&exists); err != nil {
			return fmt.Errorf("check memory node after version mismatch: %w", err)
		}
		if !exists {
			return ErrMemoryNotFound
		}
		return ErrVersionConflict
	})
	if err != nil {
		return ports.MemoryNodeRecord{}, err
	}
	return result, nil
}

func (r *MemoryRepository) LoadScopeTree(ctx context.Context, tenantID, userID, sessionID string, maxParentDepth, limit int) (ports.MemoryScopeTree, error) {
	if userID == "" || maxParentDepth < 0 || maxParentDepth > MaxParentDepth || limit < 1 || limit > MaxTreeNodes {
		return ports.MemoryScopeTree{}, ErrInvalidScopeTree
	}
	var result ports.MemoryScopeTree
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		query := `WITH RECURSIVE seed AS (
			SELECT ` + memoryNodeColumnList("m") + `
			FROM memory_nodes AS m
			WHERE m.user_id = $1::uuid
			  AND ((m.scope_type = 'user-global' AND m.scope_id = $1::uuid)
			    OR (m.scope_type = 'session' AND m.session_id = NULLIF($2, '')::uuid))
AND ($2 = '' OR EXISTS (SELECT 1 FROM sessions AS requested_session WHERE requested_session.id = NULLIF($2, '')::uuid AND requested_session.user_id = $1::uuid))
			  AND m.status IN ('active', 'stable')
			  AND m.deleted_at IS NULL
			  AND (m.expires_at IS NULL OR m.expires_at > now())
			  AND m.default_retrieval = true
		  ORDER BY m.updated_at DESC, m.id
		  LIMIT $3
		), tree AS (
			SELECT ` + memoryNodeColumnList("seed") + `, 0 AS parent_depth FROM seed
			UNION ALL
			SELECT ` + memoryNodeColumnList("parent") + `, tree.parent_depth + 1
			FROM tree
			JOIN memory_nodes AS parent ON parent.id = tree.parent_id
			WHERE tree.parent_depth < $4
			  AND parent.user_id = $1::uuid
			  AND parent.scope_type = tree.scope_type
			  AND parent.scope_id = tree.scope_id
			  AND parent.status IN ('active', 'stable')
			  AND parent.deleted_at IS NULL
			  AND (parent.expires_at IS NULL OR parent.expires_at > now())
		)
		SELECT DISTINCT ON (id) ` + memoryNodeColumns + `, parent_depth
		FROM tree
		ORDER BY id, parent_depth`

		rows, err := tx.QueryContext(ctx, query, userID, sessionID, limit, maxParentDepth)
		if err != nil {
			return fmt.Errorf("query visible memory scopes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			node, err := scanMemoryWithDepth(rows)
			if err != nil {
				return fmt.Errorf("scan memory scope tree: %w", err)
			}
			result.Nodes = append(result.Nodes, node)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate memory scope tree: %w", err)
		}
		if len(result.Nodes) == 0 {
			return nil
		}
		ids := make([]string, 0, len(result.Nodes))
		for _, node := range result.Nodes {
			ids = append(ids, node.ID)
		}
		placeholdersA, args := placeholders(ids, 0)
		placeholdersB, argsB := placeholders(ids, len(args))
		args = append(args, argsB...)
		relationQuery := `SELECT id, source_memory_id, target_memory_id, relation_type, confidence, created_at
			FROM memory_relations
			WHERE source_memory_id IN (` + placeholdersA + `)
			  AND target_memory_id IN (` + placeholdersB + `)
			ORDER BY created_at, id`
		relationRows, err := tx.QueryContext(ctx, relationQuery, args...)
		if err != nil {
			return fmt.Errorf("query memory relations: %w", err)
		}
		defer relationRows.Close()
		for relationRows.Next() {
			var relation ports.MemoryRelationRecord
			if err := relationRows.Scan(&relation.ID, &relation.SourceMemoryID, &relation.TargetMemoryID,
				&relation.RelationType, &relation.Confidence, &relation.CreatedAt); err != nil {
				return fmt.Errorf("scan memory relation: %w", err)
			}
			result.Relations = append(result.Relations, relation)
		}
		return relationRows.Err()
	})
	if err != nil {
		return ports.MemoryScopeTree{}, err
	}
	return result, nil
}

func memoryNodeColumnList(alias string) string {
	columns := strings.Split(memoryNodeColumns, ",")
	for index, column := range columns {
		columns[index] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}

func placeholders(ids []string, offset int) (string, []any) {
	parts := make([]string, len(ids))
	args := make([]any, 0, len(ids))
	for index, id := range ids {
		parts[index] = fmt.Sprintf("$%d", offset+index+1)
		args = append(args, id)
	}
	return strings.Join(parts, ", "), args
}

func scanMemoryWithDepth(row rowScanner) (ports.MemoryNodeRecord, error) {
	var node ports.MemoryNodeRecord
	var sessionID, parentID sql.NullString
	var expiresAt, deletedAt sql.NullTime
	var applicability, content, provenance []byte
	err := row.Scan(
		&node.ID, &node.IdempotencyKey, &node.UserID, &sessionID, &node.ScopeType, &node.ScopeID,
		&parentID, &node.MemoryType, &node.Status, &node.Visibility, &node.Confidence,
		&applicability, &content, &node.ContentText, &node.DefaultRetrieval, &node.Version,
		&node.CreatedAt, &node.UpdatedAt, &provenance, &expiresAt, &deletedAt, &node.ParentDepth,
	)
	if err != nil {
		return ports.MemoryNodeRecord{}, err
	}
	if sessionID.Valid {
		node.SessionID = sessionID.String
	}
	if parentID.Valid {
		node.ParentID = parentID.String
	}
	node.Applicability = append(json.RawMessage(nil), applicability...)
	node.Content = append(json.RawMessage(nil), content...)
	node.Provenance = append(json.RawMessage(nil), provenance...)
	if expiresAt.Valid {
		node.ExpiresAt = &expiresAt.Time
	}
	if deletedAt.Valid {
		node.DeletedAt = &deletedAt.Time
	}
	return node, nil
}

func getMemory(ctx context.Context, tx *sql.Tx, nodeID string) (ports.MemoryNodeRecord, error) {
	result, err := scanMemory(tx.QueryRowContext(ctx, `SELECT `+memoryNodeColumns+` FROM memory_nodes WHERE id = $1`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return ports.MemoryNodeRecord{}, ErrMemoryNotFound
	}
	if err != nil {
		return ports.MemoryNodeRecord{}, fmt.Errorf("get memory node: %w", err)
	}
	return result, nil
}

func getByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (ports.MemoryNodeRecord, error) {
	result, err := scanMemory(tx.QueryRowContext(ctx, `SELECT `+memoryNodeColumns+` FROM memory_nodes WHERE idempotency_key = $1`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return ports.MemoryNodeRecord{}, ErrMemoryNotFound
	}
	if err != nil {
		return ports.MemoryNodeRecord{}, fmt.Errorf("load idempotent memory node: %w", err)
	}
	return result, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemory(row rowScanner) (ports.MemoryNodeRecord, error) {
	var node ports.MemoryNodeRecord
	var sessionID, parentID sql.NullString
	var expiresAt, deletedAt sql.NullTime
	var applicability, content, provenance []byte
	err := row.Scan(
		&node.ID, &node.IdempotencyKey, &node.UserID, &sessionID, &node.ScopeType, &node.ScopeID,
		&parentID, &node.MemoryType, &node.Status, &node.Visibility, &node.Confidence,
		&applicability, &content, &node.ContentText, &node.DefaultRetrieval, &node.Version,
		&node.CreatedAt, &node.UpdatedAt, &provenance, &expiresAt, &deletedAt,
	)
	if err != nil {
		return ports.MemoryNodeRecord{}, err
	}
	if sessionID.Valid {
		node.SessionID = sessionID.String
	}
	if parentID.Valid {
		node.ParentID = parentID.String
	}
	node.Applicability = append(json.RawMessage(nil), applicability...)
	node.Content = append(json.RawMessage(nil), content...)
	node.Provenance = append(json.RawMessage(nil), provenance...)
	if expiresAt.Valid {
		node.ExpiresAt = &expiresAt.Time
	}
	if deletedAt.Valid {
		node.DeletedAt = &deletedAt.Time
	}
	return node, nil
}

func validMemoryScope(node ports.MemoryNodeRecord) bool {
	switch node.ScopeType {
	case "user-global":
		return node.ScopeID == node.UserID && node.SessionID == ""
	case "session":
		return node.SessionID != "" && node.ScopeID == node.SessionID
	default:
		return false
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ListMemories lists memories by scope and status for the console, including
// candidate-status nodes that the default-retrieval scope tree hides.
func (r *MemoryRepository) ListMemories(ctx context.Context, tenantID string, request ports.MemoryListRequest) (ports.MemoryListPage, error) {
	if request.UserID == "" || len(request.Statuses) == 0 {
		return ports.MemoryListPage{}, ErrInvalidScopeTree
	}
	page := request.Page
	if page < 1 {
		page = 1
	}
	size := request.PageSize
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	scope := ` m.user_id = $1::uuid
		AND ((m.scope_type = 'user-global' AND m.scope_id = $1::uuid)
		  OR (m.scope_type = 'session' AND m.session_id = NULLIF($2, '')::uuid))
		AND m.status = ANY($3::text[])
		AND m.deleted_at IS NULL`
	statuses := uuidArrayLiteral(request.Statuses)
	result := ports.MemoryListPage{Page: page, PageSize: size}
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes AS m WHERE`+scope, request.UserID, request.SessionID, statuses).Scan(&result.Total); err != nil {
			return fmt.Errorf("count memories: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+memoryNodeColumnList("m")+` FROM memory_nodes AS m WHERE`+scope+`
			ORDER BY m.updated_at DESC, m.id
			LIMIT $4 OFFSET $5`, request.UserID, request.SessionID, statuses, size, (page-1)*size)
		if err != nil {
			return fmt.Errorf("list memories: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			node, err := scanMemory(rows)
			if err != nil {
				return fmt.Errorf("scan memory: %w", err)
			}
			result.Items = append(result.Items, node)
		}
		return rows.Err()
	})
	if err != nil {
		return ports.MemoryListPage{}, err
	}
	return result, nil
}
