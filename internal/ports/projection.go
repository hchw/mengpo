package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrInvalidProjectionRecord = errors.New("invalid projection record")

type ProjectionEvent struct {
	ID               string
	RequestID        string
	UserID           string
	SessionID        string
	Mode             string
	SelectedIDs      []string
	SelectionReasons json.RawMessage
	ExcludedReasons  json.RawMessage
	Budget           json.RawMessage
	Provenance       json.RawMessage
	DegradedMode     string
	CreatedAt        time.Time
}

type ProjectionCacheEntry struct {
	CacheKey  string
	UserID    string
	SessionID string
	ScopeType string
	Response  json.RawMessage
	ExpiresAt time.Time
}

type ProjectionRepository interface {
	RecordProjection(ctx context.Context, tenantID string, event ProjectionEvent) error
	GetProjectionCache(ctx context.Context, tenantID, cacheKey, userID, sessionID, scopeType string) (json.RawMessage, bool, error)
	PutProjectionCache(ctx context.Context, tenantID string, entry ProjectionCacheEntry) error
}
