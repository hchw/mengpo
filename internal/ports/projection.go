package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidProjectionRecord = errors.New("invalid projection record")
	// ErrProjectionNotFound reports that a referenced projection does not exist
	// in the caller's tenant.
	ErrProjectionNotFound = errors.New("projection not found")
)

// ProjectionLookup is the minimal projection identity needed to associate an
// observation with the context a caller actually received. It is deliberately
// narrow: a trace reference must not expose the projection's full contents.
type ProjectionLookup struct {
	ID                string
	UserID            string
	SessionID         string
	SelectedMemoryIDs []string
}

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
	FindProjection(ctx context.Context, tenantID, projectionID string) (ProjectionLookup, error)
	GetProjectionCache(ctx context.Context, tenantID, cacheKey, userID, sessionID, scopeType string) (json.RawMessage, bool, error)
	PutProjectionCache(ctx context.Context, tenantID string, entry ProjectionCacheEntry) error
}
