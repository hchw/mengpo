package ports

import (
	"context"
	"encoding/json"
	"time"
)

// MemoryNodeRecord is the persistence-facing shape used across the repository
// boundary. The domain package owns business behavior; the adapter owns SQL.
type MemoryNodeRecord struct {
	ID               string
	IdempotencyKey   string
	UserID           string
	SessionID        string
	ScopeType        string
	ScopeID          string
	ParentID         string
	ParentDepth      int
	MemoryType       string
	Status           string
	Visibility       string
	Confidence       float64
	Applicability    json.RawMessage
	Content          json.RawMessage
	ContentText      string
	DefaultRetrieval bool
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type MemoryRelationRecord struct {
	ID             string
	SourceMemoryID string
	TargetMemoryID string
	RelationType   string
	Confidence     float64
	CreatedAt      time.Time
}

type MemoryScopeTree struct {
	Nodes     []MemoryNodeRecord
	Relations []MemoryRelationRecord
}

type MemoryNodeRepository interface {
	Create(ctx context.Context, tenantID string, node MemoryNodeRecord) (MemoryNodeRecord, error)
	Get(ctx context.Context, tenantID, nodeID string) (MemoryNodeRecord, error)
	Update(ctx context.Context, tenantID string, node MemoryNodeRecord, expectedVersion int64) (MemoryNodeRecord, error)
	LoadScopeTree(ctx context.Context, tenantID, userID, sessionID string, maxParentDepth, limit int) (MemoryScopeTree, error)
}
