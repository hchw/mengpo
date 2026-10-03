package ports

import "context"

// WorkingMemoryRecord is a session-scoped working memory together with the
// evidence that supports it. Unlike a candidate, working memory is produced and
// queried inside one session, so it is active on arrival and never waits for
// the governance gate.
type WorkingMemoryRecord struct {
	Node     MemoryNodeRecord
	Evidence []MemoryEvidenceRecord
}

// WorkingMemoryWriter persists session-scoped working memory with its evidence
// in one tenant-scoped transaction. It is idempotent on the node's idempotency
// key and the evidence uniqueness constraint.
//
// Scope is part of the contract: it must reject anything that is not session
// scope or not active. The durable user-global tree is written through the
// candidate writer and promoted only by governance.
type WorkingMemoryWriter interface {
	PersistWorkingMemory(ctx context.Context, tenantID string, records []WorkingMemoryRecord) (int, error)
}
