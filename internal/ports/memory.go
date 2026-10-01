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
	Provenance       json.RawMessage
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        *time.Time
	DeletedAt        *time.Time
}

type MemoryGovernanceAuditRecord struct {
	ID           string
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	RequestID    string
	Changes      json.RawMessage
	CreatedAt    time.Time
}

type MemoryGovernanceMutationRecord struct {
	Action              string
	ExpectedVersion     int64
	Memory              MemoryNodeRecord
	Replacement         *MemoryNodeRecord
	Relation            *MemoryRelationRecord
	Audit               MemoryGovernanceAuditRecord
	PurgeDerivedData    bool
	InvalidateEmbedding bool
}

type MemoryGovernanceMutationResult struct {
	Memory      MemoryNodeRecord
	Replacement *MemoryNodeRecord
	Relation    *MemoryRelationRecord
	Audit       MemoryGovernanceAuditRecord
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

type MemoryGovernanceMutationRepository interface {
	LookupGovernanceMutation(ctx context.Context, tenantID, auditID, resourceID, actorType, actorID, action, requestID string) (MemoryGovernanceMutationResult, bool, error)
	ApplyGovernanceMutation(ctx context.Context, tenantID string, mutation MemoryGovernanceMutationRecord) (MemoryGovernanceMutationResult, error)
}

// MemoryListRequest filters a memory list by scope and status for the console.
type MemoryListRequest struct {
	UserID    string
	SessionID string
	Statuses  []string
	Page      int
	PageSize  int
}

// MemoryListPage is one paginated page of memories.
type MemoryListPage struct {
	Items    []MemoryNodeRecord
	Total    int64
	Page     int
	PageSize int
}

// MemoryListRepository lists memories by scope and status. It exists so the
// console can show candidates, which the default-retrieval scope tree excludes.
type MemoryListRepository interface {
	ListMemories(ctx context.Context, tenantID string, request MemoryListRequest) (MemoryListPage, error)
}
