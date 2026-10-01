package ports

import (
	"context"
	"errors"
)

var ErrInvalidRerankRequest = errors.New("invalid rerank request")

// RerankerMetadata identifies the independent cross-encoder model so ranking
// provenance can be recorded alongside embedding identity.
type RerankerMetadata struct {
	Model   string
	Version string
}

// RerankCandidate is one document submitted to the cross-encoder. ID is the
// caller's candidate identifier (usually the memory node id).
type RerankCandidate struct {
	ID   string
	Text string
}

// RerankResult carries the cross-encoder relevance for one candidate.
type RerankResult struct {
	ID        string
	Relevance float64
}

// Reranker is the independent ranking model port. It is deliberately separate
// from the Embedder: embedding similarity only proposes candidates and can
// never be the final ranker or a transfer decision on its own.
type Reranker interface {
	Rank(ctx context.Context, query string, candidates []RerankCandidate) ([]RerankResult, error)
	Metadata() RerankerMetadata
}
