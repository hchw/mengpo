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

// RetrievalEvent is one projection request's telemetry: what was considered,
// what was selected, whether the cache served it, and how long it took. Every
// projection records one, hit or miss, so cache effectiveness is measurable.
type RetrievalEvent struct {
	ID              string
	RequestID       string
	UserID          string
	SessionID       string
	QueryHash       string
	Mode            string
	CandidateCount  int
	SelectedIDs     []string
	ExcludedReasons json.RawMessage
	LatencyMS       int
	TokensInjected  int
	CacheHit        bool
	DegradedMode    string
	CreatedAt       time.Time
}

// RetrievalTelemetryWriter records retrieval telemetry. Recording is
// best-effort: a projection must not fail because its trace could not be
// persisted.
type RetrievalTelemetryWriter interface {
	RecordRetrievalEvent(ctx context.Context, tenantID string, event RetrievalEvent) error
}

// RetrievalMetrics aggregates retrieval telemetry and its linked feedback over
// a window. Rates are zero when there is no sample; the *Samples counters let
// the caller tell "0%" apart from "never measured". RetrievalPrecision is
// feedback-labelled: of the memories that were injected and later received a
// positive or negative signal, the fraction that was positive.
type RetrievalMetrics struct {
	Projections         int
	CacheHits           int
	CacheHitRate        float64
	LatencyP95Ms        int
	TokensPerProjection float64
	FeedbackSamples     int
	PositiveFeedback    int
	NegativeFeedback    int
	RetrievalPrecision  float64
	WrongMemoryRate     float64
}

// RetrievalMetricsReader computes retrieval metrics from recorded telemetry.
type RetrievalMetricsReader interface {
	ComputeRetrievalMetrics(ctx context.Context, tenantID string, since time.Time) (RetrievalMetrics, error)
}
