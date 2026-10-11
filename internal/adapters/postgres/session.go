package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// idleSessionPredicate is the definition of an activity-free session: it must
// own no event, memory, run or retrieval record. Every table that references
// sessions is checked so cleanup can never drop attribution data (memory_nodes
// and retrieval_cache cascade on delete; the rest null out the reference).
const idleSessionPredicate = `
	NOT EXISTS (SELECT 1 FROM observed_events AS e WHERE e.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM memory_nodes AS m WHERE m.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM analysis_jobs AS j WHERE j.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM extraction_runs AS x WHERE x.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM memory_feedback AS f WHERE f.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM retrieval_events AS r WHERE r.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM projection_events AS p WHERE p.session_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM retrieval_cache AS c WHERE c.session_id = s.id)`

// ListIdleSessions returns sessions older than the cutoff that never produced
// any activity, oldest first.
func (r *SessionRepository) ListIdleSessions(ctx context.Context, tenantID string, olderThan time.Time, limit int) ([]ports.SessionSummaryRecord, error) {
	if tenantID == "" || olderThan.IsZero() {
		return nil, ports.ErrSessionNotFound
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var items []ports.SessionSummaryRecord
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.id::text, s.title, s.status, s.started_at, s.updated_at
			FROM sessions AS s
			WHERE s.started_at < $1 AND`+idleSessionPredicate+`
			ORDER BY s.started_at
			LIMIT $2`, olderThan, limit)
		if err != nil {
			return fmt.Errorf("list idle sessions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item ports.SessionSummaryRecord
			if err := rows.Scan(&item.ID, &item.Title, &item.Status, &item.StartedAt, &item.UpdatedAt); err != nil {
				return fmt.Errorf("scan idle session: %w", err)
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteIdleSession removes a session only when it is still older than the
// cutoff and still activity-free. It returns false when the session gained
// activity or already vanished.
func (r *SessionRepository) DeleteIdleSession(ctx context.Context, tenantID, sessionID string, olderThan time.Time) (bool, error) {
	if tenantID == "" || sessionID == "" || olderThan.IsZero() {
		return false, ports.ErrSessionNotFound
	}
	removed := false
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM sessions AS s
			WHERE s.id = $1::uuid AND s.started_at < $2 AND`+idleSessionPredicate, sessionID, olderThan)
		if err != nil {
			return fmt.Errorf("delete idle session: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("idle session rows affected: %w", err)
		}
		removed = affected > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
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
	if request.WithMemoriesOnly {
		// A bound session only appears once it owns a live session-scoped memory;
		// candidate and other non-deleted rows count, matching the explorer detail.
		scope += ` AND EXISTS (SELECT 1 FROM memory_nodes AS m WHERE m.session_id = sessions.id AND m.scope_type = 'session' AND m.deleted_at IS NULL)`
	}
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
