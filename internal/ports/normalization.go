package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrInvalidNormalizedEvent = errors.New("invalid normalized event record")

// NormalizedEventRecord is one normalized observation ready to persist. The ID
// must be a UUID; the analysis pipeline's logical ids are not database keys.
type NormalizedEventRecord struct {
	ID                   string
	RawEventID           string
	SchemaVersion        string
	NormalizationVersion string
	Sequence             *int64
	ParentEventID        string
	Payload              json.RawMessage
}

// NormalizedEventStore persists normalized events and advances the raw event's
// processing status inside one tenant transaction. It is idempotent on
// raw_event_id so re-delivered jobs never duplicate rows.
type NormalizedEventStore interface {
	StoreNormalizedEvents(ctx context.Context, tenantID string, events []NormalizedEventRecord) error
	MarkRawEventProcessed(ctx context.Context, tenantID, rawEventID string) error
}

// MemberRecord is one tenant member as shown in the console.
type MemberRecord struct {
	UserID string
	Email  string
	Role   string
	Status string
}

// PendingAnalysisEvent is a normalized observation selected for periodic
// consolidation. RawEventID is the evidence id used in analysis batches.
type PendingAnalysisEvent struct {
	NormalizedID string
	RawEventID   string
	SessionID    string
	OccurredAt   time.Time
	Payload      json.RawMessage
	Sequence     *int64
}

// NormalizedEventReader selects normalized events for periodic maintenance. It
// reads; it never writes memory.
type NormalizedEventReader interface {
	ListPendingAnalysisEvents(ctx context.Context, tenantID string, offset, limit int) ([]PendingAnalysisEvent, error)
	LoadAnalysisEventsByRawIDs(ctx context.Context, tenantID string, rawEventIDs []string) ([]PendingAnalysisEvent, error)
}
