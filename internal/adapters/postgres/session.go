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

// SessionRepository provides tenant-scoped session ownership checks used by
// memory-scope authorization.
type SessionRepository struct {
	router *tenantdb.Router
}

func NewSessionRepository(router *tenantdb.Router) *SessionRepository {
	return &SessionRepository{router: router}
}

func (r *SessionRepository) GetSessionOwner(ctx context.Context, tenantID, sessionID string) (string, error) {
	if tenantID == "" || sessionID == "" {
		return "", ports.ErrSessionNotFound
	}
	var ownerID string
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id = $1`, sessionID).Scan(&ownerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("get session owner: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return ownerID, nil
}

// BindSession creates or reuses the session for a bound Agent identity. When
// ExternalID is set the mapping is idempotent, so an Agent retry with the same
// external identity returns the previously bound session.
func (r *SessionRepository) BindSession(ctx context.Context, binding ports.SessionBinding) (string, error) {
	if binding.TenantID == "" || binding.SessionID == "" || binding.UserID == "" {
		return "", ports.ErrSessionNotFound
	}
	metadata := map[string]string{}
	if binding.ExternalID != "" {
		metadata["external_id"] = binding.ExternalID
	}
	if binding.AgentID != "" {
		metadata["agent_id"] = binding.AgentID
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	boundID := binding.SessionID
	err = r.router.WithTenantTx(ctx, binding.TenantID, func(tx *sql.Tx) error {
		if binding.ExternalID != "" {
			var existing string
			err := tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE user_id=$1::uuid AND metadata->>'external_id'=$2 LIMIT 1`, binding.UserID, binding.ExternalID).Scan(&existing)
			if err == nil {
				boundID = existing
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("lookup session by external id: %w", err)
			}
		}
		var agentID any
		if binding.AgentID != "" {
			agentID = binding.AgentID
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, created_by_agent_id, status, title, metadata)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'active', $4, $5::jsonb)
			ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title`, binding.SessionID, binding.UserID, agentID, binding.Title, string(encoded))
		if err != nil {
			return fmt.Errorf("bind session: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return boundID, nil
}

// ListSessions lists tenant sessions for the console, newest first, optionally
// filtered by user and status.
func (r *SessionRepository) ListSessions(ctx context.Context, tenantID string, request ports.SessionListRequest) (ports.SessionListPage, error) {
	if tenantID == "" {
		return ports.SessionListPage{}, ports.ErrSessionNotFound
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
	statuses := "{}"
	if len(request.Statuses) > 0 {
		statuses = uuidArrayLiteral(request.Statuses)
	}
	scope := ` ($1 = '' OR user_id = NULLIF($1, '')::uuid) AND (cardinality($2::text[]) = 0 OR status = ANY($2::text[]))`
	result := ports.SessionListPage{Page: page, PageSize: size}
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE`+scope, request.UserID, statuses).Scan(&result.Total); err != nil {
			return fmt.Errorf("count sessions: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT id::text, title, status, started_at, updated_at FROM sessions WHERE`+scope+` ORDER BY updated_at DESC, id LIMIT $3 OFFSET $4`, request.UserID, statuses, size, (page-1)*size)
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item ports.SessionSummaryRecord
			if err := rows.Scan(&item.ID, &item.Title, &item.Status, &item.StartedAt, &item.UpdatedAt); err != nil {
				return fmt.Errorf("scan session: %w", err)
			}
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return ports.SessionListPage{}, err
	}
	return result, nil
}
