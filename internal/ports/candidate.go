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
