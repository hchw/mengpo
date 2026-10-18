package ports

import "context"

// SemanticNeighbor is a memory node together with its cosine similarity to a
// reference memory in the same scope.
type SemanticNeighbor struct {
	Node       MemoryNodeRecord
	Similarity float64
}

// SemanticNeighborQuery selects the near neighbors of one reference memory.
// The reference's user, scope, and embedding identity are resolved from the
// stored node, so callers never supply them (and cannot compare across scope or
// across embedding models by mistake). Threshold is the minimum cosine
// similarity, and Limit bounds the returned neighbors.
type SemanticNeighborQuery struct {
	ReferenceMemoryID string
	Threshold         float64
	Limit             int
	// LexicalFallback asks the reader to compare canonical text with trigram
	// similarity when the reference has no usable embedding. It never treats
	// lexical similarity as embedding similarity.
	LexicalFallback bool
}

// SemanticNeighborReader lists memories semantically close to a reference
// memory within the same user and scope, using embeddings that match the
// reference's model identity. It backs near-duplicate grouping and merging. It
// also reports embedding readiness so callers can record degraded comparisons
// instead of mistaking "cannot compare" for "not similar".
type SemanticNeighborReader interface {
	ListSemanticNeighbors(ctx context.Context, tenantID string, query SemanticNeighborQuery) ([]SemanticNeighbor, error)
	// EmbeddingReady reports whether the memory has a usable embedding for the
	// current model identity.
	EmbeddingReady(ctx context.Context, tenantID, memoryID string) (bool, error)
}
