package ports

import "context"

type VectorSearchResult struct {
	Node     MemoryNodeRecord
	Distance float64
}

type EmbeddingRepository interface {
	SaveEmbedding(ctx context.Context, tenantID, memoryID, modelID, artifact, version string, vector []float32) error
	SearchSimilar(ctx context.Context, tenantID, userID, sessionID, modelID, artifact, version string, vector []float32, limit int) ([]VectorSearchResult, error)
}
