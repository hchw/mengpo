package ports

import (
	"context"
	"errors"
)

// ErrMemoryNotFound is returned by repositories when a memory node does not
// exist (anymore) within the requested tenant schema.
var ErrMemoryNotFound = errors.New("memory node not found")

type MemorySearchRequest struct {
	Query      string
	MemoryType string
	Page       int
	PageSize   int
}

type MemorySearchResult struct {
	Node  MemoryNodeRecord
	Score float64
}

type MemorySearchPage struct {
	Items    []MemorySearchResult
	Total    int64
	Page     int
	PageSize int
}

type MemorySearchRepository interface {
	Search(ctx context.Context, tenantID, userID, sessionID string, request MemorySearchRequest) (MemorySearchPage, error)
}

// RelatedMemory is a memory reached by traversing a relation from a seed
// candidate. The relation itself is only evidence metadata; it never becomes a
// scope boundary.
type RelatedMemory struct {
	Node         MemoryNodeRecord
	RelationType string
	Confidence   float64
}

type RelationRepository interface {
	// ListRelated expands seed memory IDs through memory_relations in both
	// directions, restricted to the same tenant schema.
	ListRelated(ctx context.Context, tenantID string, memoryIDs []string, limit int) ([]RelatedMemory, error)
}

// RecallChannel names the candidate source. A candidate recalled by several
// channels keeps every channel label so later ranking can explain coverage.
const (
	RecallChannelStructured = "structured"
	RecallChannelFullText   = "full-text"
	RecallChannelVector     = "vector"
	RecallChannelRelation   = "relation"
	RecallChannelParent     = "parent"
)

type RecallCandidate struct {
	Node     MemoryNodeRecord
	Channels []string
	// Score keeps the best channel score (0 for structural channels).
	Score float64
}
