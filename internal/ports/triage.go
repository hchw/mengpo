package ports

import (
	"context"
	"time"
)

// CandidateTriageRecord is an advisory triage finding for one candidate. It is
// append-only and never changes the candidate's state: a human or the evidence
// policy still owns promotion.
type CandidateTriageRecord struct {
	ID             string
	MemoryID       string
	RunID          string
	DuplicateGroup string
	ConflictIDs    []string
	Priority       int
	Rationale      string
	CreatedAt      time.Time
}

// CandidateTriageWriter persists triage artifacts.
type CandidateTriageWriter interface {
	PersistCandidateTriage(ctx context.Context, tenantID string, records []CandidateTriageRecord) error
}

// CandidateTriageReader reads the latest triage finding for each memory, so the
// review surface can show the model's advisory suggestion next to the evidence.
type CandidateTriageReader interface {
	LatestCandidateTriage(ctx context.Context, tenantID string, memoryIDs []string) (map[string]CandidateTriageRecord, error)
}

// MemoryRelationWriter records memory-to-memory lineage without a governance
// transition. It is used for the `generalizes` edge from an abstracted candidate
// to its sources, and never changes memory state.
type MemoryRelationWriter interface {
	AddMemoryRelations(ctx context.Context, tenantID string, relations []MemoryRelationRecord) error
}
