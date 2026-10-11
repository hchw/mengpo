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

// RecordRetrievalEvent persists one projection request's telemetry.
func (r *ProjectionRepository) RecordRetrievalEvent(ctx context.Context, tenantID string, event ports.RetrievalEvent) error {
	if tenantID == "" || event.ID == "" || event.UserID == "" || event.RequestID == "" || event.QueryHash == "" || (event.Mode != "focus" && event.Mode != "diverge") {
		return ports.ErrInvalidProjectionRecord
	}
	excluded := event.ExcludedReasons
	if len(excluded) == 0 {
		excluded = json.RawMessage(`{}`)
	}
	if !json.Valid(excluded) {
		return ports.ErrInvalidProjectionRecord
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO retrieval_events
			(id,user_id,session_id,request_id,query_hash,mode,candidate_count,selected_memory_ids,excluded_reasons,degraded_mode,latency_ms,cache_hit,tokens_injected)
			VALUES($1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8::uuid[],$9::jsonb,NULLIF($10,''),$11,$12,$13)`,
			event.ID, event.UserID, event.SessionID, event.RequestID, event.QueryHash, event.Mode,
			event.CandidateCount, uuidArrayLiteral(event.SelectedIDs), string(excluded), event.DegradedMode,
			event.LatencyMS, event.CacheHit, event.TokensInjected)
		if err != nil {
			return fmt.Errorf("record retrieval event: %w", err)
		}
		return nil
	})
}

// ComputeRetrievalMetrics aggregates retrieval telemetry and the feedback left
// on injected memories over the window starting at since. Cache hit rate and
// precision are only meaningful when Projections/FeedbackSamples are non-zero;
// callers must not present a bare 0% as a quality signal without them.
func (r *ProjectionRepository) ComputeRetrievalMetrics(ctx context.Context, tenantID string, since time.Time) (ports.RetrievalMetrics, error) {
	var metrics ports.RetrievalMetrics
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var projections, cacheHits, latencyP95 int
		var tokensSum int64
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*),
				count(*) FILTER (WHERE cache_hit),
				COALESCE(sum(tokens_injected), 0),
				COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)
			FROM retrieval_events WHERE created_at >= $1`, since).Scan(&projections, &cacheHits, &tokensSum, &latencyP95); err != nil {
			return fmt.Errorf("aggregate retrieval events: %w", err)
		}
		metrics.Projections = projections
		metrics.CacheHits = cacheHits
		metrics.LatencyP95Ms = latencyP95
		if projections > 0 {
			metrics.CacheHitRate = float64(cacheHits) / float64(projections)
			metrics.TokensPerProjection = float64(tokensSum) / float64(projections)
		}

		var positive, negative int
		if err := tx.QueryRowContext(ctx, `
			WITH injected AS (
				SELECT DISTINCT unnest(selected_memory_ids) AS memory_id
				FROM retrieval_events WHERE created_at >= $1
			)
			SELECT
				count(*) FILTER (WHERE f.feedback_type IN ('accepted','helpful')),
				count(*) FILTER (WHERE f.feedback_type IN ('harmful','corrected','conflict','expired'))
			FROM memory_feedback f JOIN injected i ON i.memory_id = f.memory_id
			WHERE f.created_at >= $1`, since).Scan(&positive, &negative); err != nil {
			return fmt.Errorf("aggregate retrieval feedback: %w", err)
		}
		metrics.PositiveFeedback = positive
		metrics.NegativeFeedback = negative
		metrics.FeedbackSamples = positive + negative
		if metrics.FeedbackSamples > 0 {
			metrics.RetrievalPrecision = float64(positive) / float64(metrics.FeedbackSamples)
			metrics.WrongMemoryRate = float64(negative) / float64(metrics.FeedbackSamples)
		}
		return nil
	})
	if err != nil {
		return ports.RetrievalMetrics{}, err
	}
	return metrics, nil
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
