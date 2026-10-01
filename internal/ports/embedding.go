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

type EmbeddingJob struct {
	ID             string
	MemoryID       string
	ModelID        string
	Artifact       string
	ModelVersion   string
	Status         string
	Attempts       int
	IdempotencyKey string
	LastError      string
	ContentText    string
}

type EmbeddingJobRepository interface {
	// EnqueueEmbeddingJob creates an idempotent embedding job. Re-enqueueing a
	// finished job key resets it to queued so rebuilds can replay work.
	EnqueueEmbeddingJob(ctx context.Context, tenantID, memoryID, modelID, artifact, version, idempotencyKey string) error
	// RebuildForModel marks every memory whose stored embedding identity differs
	// from the target model as stale and enqueues embedding jobs for all
	// non-deleted memories. Used when the embedding model version switches.
	RebuildForModel(ctx context.Context, tenantID, modelID, artifact, version string) (int, error)
	// ClaimEmbeddingJobs atomically claims up to limit queued/retrying jobs
	// (running state) and returns them with the memory content text to embed.
	ClaimEmbeddingJobs(ctx context.Context, tenantID string, limit int) ([]EmbeddingJob, error)
	CompleteEmbeddingJob(ctx context.Context, tenantID, jobID string) error
	// RetryEmbeddingJob records a failure: jobs below maxAttempts go back to
	// retrying; exhausted jobs become failed and their memory embedding_status
	// is set to failed.
	RetryEmbeddingJob(ctx context.Context, tenantID, jobID string, lastErr string, maxAttempts int) error
}
