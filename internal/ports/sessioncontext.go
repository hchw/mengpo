package ports

import "context"

// SessionContextItem is a piece of already-authorized local session context
// that can be injected when the memory service is degraded. It is not a memory
// node: it never enters governance or consolidation, only the current
// projection.
type SessionContextItem struct {
	MemoryID string
	Text     string
	// Local marks context that comes from the caller's own session store.
	Local bool
}

// SessionContextProvider returns the caller's local session context so the
// memory service can degrade to it on timeout or database unavailability.
type SessionContextProvider interface {
	LocalContext(ctx context.Context, tenantID, userID, sessionID string) ([]SessionContextItem, error)
}
