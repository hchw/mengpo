package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

const MaxProjectionCacheResponseBytes = 1 << 20

var ErrProjectionCacheMiss = errors.New("projection cache miss")

type ProjectionRepository struct{ router *tenantdb.Router }

func NewProjectionRepository(router *tenantdb.Router) *ProjectionRepository {
	return &ProjectionRepository{router: router}
}

func (r *ProjectionRepository) RecordProjection(ctx context.Context, tenantID string, event ports.ProjectionEvent) error {
	if tenantID == "" || event.RequestID == "" || event.UserID == "" || (event.Mode != "focus" && event.Mode != "diverge") || len(event.SelectionReasons) == 0 || !json.Valid(event.SelectionReasons) || len(event.ExcludedReasons) == 0 || !json.Valid(event.ExcludedReasons) || len(event.Budget) == 0 || !json.Valid(event.Budget) || len(event.Provenance) == 0 || !json.Valid(event.Provenance) {
		return ports.ErrInvalidProjectionRecord
	}
	if event.ID == "" {
		return ports.ErrInvalidProjectionRecord
	}
	for _, id := range event.SelectedIDs {
		if id == "" {
			return ports.ErrInvalidProjectionRecord
		}
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projection_events
			(id,request_id,user_id,session_id,mode,selected_memory_ids,selection_reasons,excluded_reasons,budget,provenance,degraded_mode)
			VALUES($1::uuid,$2,$3::uuid,NULLIF($4,'')::uuid,$5,$6::uuid[],$7::jsonb,$8::jsonb,$9::jsonb,$10::jsonb,NULLIF($11,''))`,
			event.ID, event.RequestID, event.UserID, event.SessionID, event.Mode, uuidArrayLiteral(event.SelectedIDs), string(event.SelectionReasons), string(event.ExcludedReasons), string(event.Budget), string(event.Provenance), event.DegradedMode)
		if err != nil {
			return fmt.Errorf("record projection event: %w", err)
		}
		return nil
	})
}

func (r *ProjectionRepository) GetProjectionCache(ctx context.Context, tenantID, cacheKey, userID, sessionID, scopeType string) (json.RawMessage, bool, error) {
	if tenantID == "" || cacheKey == "" || userID == "" || (scopeType != "user-global" && scopeType != "session") {
		return nil, false, ports.ErrInvalidProjectionRecord
	}
	var response []byte
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT response FROM retrieval_cache
			WHERE cache_key=$1 AND user_id=$2::uuid AND session_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid
			AND scope_type=$4 AND expires_at>now()`, cacheKey, userID, sessionID, scopeType).Scan(&response)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProjectionCacheMiss
		}
		if err != nil {
			return fmt.Errorf("read projection cache: %w", err)
		}
		return nil
	})
	if errors.Is(err, ErrProjectionCacheMiss) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append(json.RawMessage(nil), response...), true, nil
}

func (r *ProjectionRepository) PutProjectionCache(ctx context.Context, tenantID string, entry ports.ProjectionCacheEntry) error {
	if tenantID == "" || entry.CacheKey == "" || entry.UserID == "" || (entry.ScopeType != "user-global" && entry.ScopeType != "session") || len(entry.Response) == 0 || len(entry.Response) > MaxProjectionCacheResponseBytes || !json.Valid(entry.Response) || entry.ExpiresAt.IsZero() {
		return ports.ErrInvalidProjectionRecord
	}
	if entry.ScopeType == "session" && entry.SessionID == "" {
		return ports.ErrInvalidProjectionRecord
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO retrieval_cache(cache_key,user_id,session_id,scope_type,response,expires_at)
			VALUES($1,$2::uuid,NULLIF($3,'')::uuid,$4,$5::jsonb,$6)
			ON CONFLICT(cache_key) DO UPDATE SET user_id=EXCLUDED.user_id,session_id=EXCLUDED.session_id,scope_type=EXCLUDED.scope_type,response=EXCLUDED.response,expires_at=EXCLUDED.expires_at,created_at=now()`,
			entry.CacheKey, entry.UserID, entry.SessionID, entry.ScopeType, string(entry.Response), entry.ExpiresAt)
		if err != nil {
			return fmt.Errorf("write projection cache: %w", err)
		}
		return nil
	})
}

func uuidArrayLiteral(ids []string) string {
	if len(ids) == 0 {
		return "{}"
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = `"` + strings.ReplaceAll(strings.ReplaceAll(id, `\`, `\\`), `"`, `\"`) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// FindProjection resolves a caller-supplied projection reference inside the
// tenant's schema. A reference that is malformed, unknown, or simply not there
// is reported as ErrProjectionNotFound rather than as a server failure: the
// caller may legitimately hold a reference from an environment this tenant no
// longer has.
func (r *ProjectionRepository) FindProjection(ctx context.Context, tenantID, projectionID string) (ports.ProjectionLookup, error) {
	if tenantID == "" || !looksLikeUUID(projectionID) {
		return ports.ProjectionLookup{}, ports.ErrProjectionNotFound
	}
	lookup := ports.ProjectionLookup{ID: projectionID}
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var sessionID string
		var selected string
		row := tx.QueryRowContext(ctx, `SELECT user_id::text, COALESCE(session_id::text, ''), COALESCE(array_to_string(selected_memory_ids, ','), '') FROM projection_events WHERE id = $1::uuid`, projectionID)
		if err := row.Scan(&lookup.UserID, &sessionID, &selected); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ports.ErrProjectionNotFound
			}
			return fmt.Errorf("find projection: %w", err)
		}
		lookup.SessionID = sessionID
		if selected != "" {
			for _, id := range strings.Split(selected, ",") {
				if id != "" {
					lookup.SelectedMemoryIDs = append(lookup.SelectedMemoryIDs, id)
				}
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ports.ErrProjectionNotFound) {
			return ports.ProjectionLookup{}, ports.ErrProjectionNotFound
		}
		return ports.ProjectionLookup{}, err
	}
	return lookup, nil
}

// looksLikeUUID keeps a malformed reference from reaching a uuid cast, which
// would otherwise surface as a database error instead of "not found".
func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}
