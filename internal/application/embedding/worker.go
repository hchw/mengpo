package embedding

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hchw/mengpo/internal/ports"
)

const DefaultMaxAttempts = 3
const DefaultBatchSize = 50

// Worker drains embedding jobs for one tenant using a local Embedder. It is
// the only component allowed to write memory embeddings; structural and
// full-text retrieval never depend on it.
type Worker struct {
	jobs        ports.EmbeddingJobRepository
	embeddings  ports.EmbeddingRepository
	embedder    ports.Embedder
	batchSize   int
	maxAttempts int
}

func NewWorker(jobs ports.EmbeddingJobRepository, embeddings ports.EmbeddingRepository, embedder ports.Embedder, batchSize, maxAttempts int) *Worker {
	if batchSize < 1 {
		batchSize = DefaultBatchSize
	}
	if maxAttempts < 1 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Worker{jobs: jobs, embeddings: embeddings, embedder: embedder, batchSize: batchSize, maxAttempts: maxAttempts}
}

// RebuildForModel switches the tenant to the current embedder identity: every
// memory whose stored embedding identity differs is marked stale and re-enqueued.
func (w *Worker) RebuildForModel(ctx context.Context, tenantID string) (int, error) {
	metadata := w.embedder.Metadata()
	return w.jobs.RebuildForModel(ctx, tenantID, metadata.ModelID, metadata.Artifact, metadata.Version)
}

// ProcessBatch claims up to batchSize jobs and embeds each memory content. A
// job whose recorded model identity no longer matches the current embedder is
// completed without changes (its rebuild job carries the new identity).
func (w *Worker) ProcessBatch(ctx context.Context, tenantID string) (int, error) {
	if tenantID == "" {
		return 0, errors.New("embedding worker requires a tenant id")
	}
	claimed, err := w.jobs.ClaimEmbeddingJobs(ctx, tenantID, w.batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim embedding jobs: %w", err)
	}
	metadata := w.embedder.Metadata()
	processed := 0
	for _, job := range claimed {
		if job.ModelID != metadata.ModelID || job.ModelVersion != metadata.Version || job.Artifact != metadata.Artifact {
			if err := w.jobs.CompleteEmbeddingJob(ctx, tenantID, job.ID); err != nil {
				return processed, fmt.Errorf("complete stale embedding job %s: %w", job.ID, err)
			}
			continue
		}
		if strings.TrimSpace(job.ContentText) == "" {
			if err := w.jobs.RetryEmbeddingJob(ctx, tenantID, job.ID, "memory has no embeddable content text", w.maxAttempts); err != nil {
				return processed, fmt.Errorf("fail empty embedding job %s: %w", job.ID, err)
			}
			continue
		}
		vector, err := w.embedder.Embed(ctx, job.ContentText)
		if err != nil {
			if retryErr := w.jobs.RetryEmbeddingJob(ctx, tenantID, job.ID, err.Error(), w.maxAttempts); retryErr != nil {
				return processed, fmt.Errorf("record embedding failure for job %s: %w", job.ID, retryErr)
			}
			continue
		}
		if err := w.embeddings.SaveEmbedding(ctx, tenantID, job.MemoryID, job.ModelID, job.Artifact, job.ModelVersion, vector); err != nil {
			if retryErr := w.jobs.RetryEmbeddingJob(ctx, tenantID, job.ID, err.Error(), w.maxAttempts); retryErr != nil {
				return processed, fmt.Errorf("record embedding save failure for job %s: %w", job.ID, retryErr)
			}
			continue
		}
		if err := w.jobs.CompleteEmbeddingJob(ctx, tenantID, job.ID); err != nil {
			return processed, fmt.Errorf("complete embedding job %s: %w", job.ID, err)
		}
		processed++
	}
	return processed, nil
}
