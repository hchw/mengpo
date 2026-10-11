package ports

import "context"

// MemoryEvidenceRecord links a memory node to the raw evidence that justifies
// it. RawEventID must reference an observed event in the same tenant schema.
type MemoryEvidenceRecord struct {
	ID                string
	MemoryID          string
	RawEventID        string
	NormalizedEventID string
	EvidenceRole      string
	Confidence        float64
	Attribution       string
	// Reliability and SourceSessionID are read-side projections of the source
	// event (observed_events.reliability / .session_id). The write path leaves
	// them empty; the reader fills them so promotion can evaluate trust and
	// cross-session independence without re-deriving them.
	Reliability     string
	SourceSessionID string
}

// CandidateMemoryRecord is a model-proposed memory together with its evidence.
// The writer persists it as a candidate only; promotion stays with governance.
type CandidateMemoryRecord struct {
	Node     MemoryNodeRecord
	Evidence []MemoryEvidenceRecord
}

// CandidateMemoryWriter persists model-proposed memories as candidates with
// their evidence in one tenant-scoped transaction. It is idempotent on the
// node's idempotency key and the evidence uniqueness constraint.
type CandidateMemoryWriter interface {
	PersistCandidates(ctx context.Context, tenantID string, records []CandidateMemoryRecord) (int, error)
}

// MemoryEvidenceReader reads the evidence justifying a memory node together
// with its source event's reliability and session. Promotion consumes only this
// reader, never candidate self-reported confidence.
type MemoryEvidenceReader interface {
	ListMemoryEvidence(ctx context.Context, tenantID, memoryID string) ([]MemoryEvidenceRecord, error)
}

// MemoryInfluenceReader reports which of the given sessions were influenced by
// a memory or its lineage (the memory was injected/used there). Such a session
// is an echo, not independent corroboration, so promotion excludes it.
type MemoryInfluenceReader interface {
	InfluencedSessions(ctx context.Context, tenantID, memoryID string, sessionIDs []string) (map[string]struct{}, error)
}
