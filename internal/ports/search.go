package ports

import "context"

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
