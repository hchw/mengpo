package embedding

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

type fakeJobRepository struct {
	jobs        map[string]*ports.EmbeddingJob
	nextID      int
	claimCursor int
	saved       map[string][]float32
	completed   []string
	retryCalls  []struct {
		jobID    string
		lastErr  string
		maxTries int
	}
	rebuildTarget ports.EmbeddingMetadata
	rebuildCount  int
}

func newFakeJobRepository() *fakeJobRepository {
	return &fakeJobRepository{jobs: map[string]*ports.EmbeddingJob{}, saved: map[string][]float32{}}
}

func (f *fakeJobRepository) EnqueueEmbeddingJob(ctx context.Context, tenantID, memoryID, modelID, artifact, version, idempotencyKey string) error {
	for _, job := range f.jobs {
		if job.IdempotencyKey == idempotencyKey {
			if job.Status == "succeeded" || job.Status == "failed" || job.Status == "cancelled" {
				job.Status, job.Attempts = "queued", 0
			}
			return nil
		}
	}
	f.nextID++
	id := string(rune('A' + f.nextID - 1))
	f.jobs[id] = &ports.EmbeddingJob{ID: id, MemoryID: memoryID, ModelID: modelID, Artifact: artifact, ModelVersion: version, Status: "queued", IdempotencyKey: idempotencyKey, ContentText: "content of " + memoryID}
	return nil
}

func (f *fakeJobRepository) RebuildForModel(ctx context.Context, tenantID, modelID, artifact, version string) (int, error) {
	f.rebuildTarget = ports.EmbeddingMetadata{ModelID: modelID, Artifact: artifact, Version: version}
	count := 0
	for _, job := range f.jobs {
		if job.ModelVersion != version || job.Status == "succeeded" || job.Status == "failed" {
			count++
		}
	}
	f.rebuildCount = count
	return count, nil
}

func (f *fakeJobRepository) ClaimEmbeddingJobs(ctx context.Context, tenantID string, limit int) ([]ports.EmbeddingJob, error) {
	claimed := make([]ports.EmbeddingJob, 0, limit)
	for _, job := range f.jobs {
		if len(claimed) >= limit {
			break
		}
		if job.Status == "queued" || job.Status == "retrying" {
			job.Status = "running"
			job.Attempts++
			claimed = append(claimed, *job)
		}
	}
	return claimed, nil
}

func (f *fakeJobRepository) CompleteEmbeddingJob(ctx context.Context, tenantID, jobID string) error {
	job, ok := f.jobs[jobID]
	if !ok || job.Status != "running" {
		return errors.New("job not running")
	}
	job.Status = "succeeded"
	f.completed = append(f.completed, jobID)
	return nil
}

func (f *fakeJobRepository) RetryEmbeddingJob(ctx context.Context, tenantID, jobID, lastErr string, maxAttempts int) error {
	job, ok := f.jobs[jobID]
	if !ok || job.Status != "running" {
		return errors.New("job not running")
	}
	f.retryCalls = append(f.retryCalls, struct {
		jobID    string
		lastErr  string
		maxTries int
	}{jobID, lastErr, maxAttempts})
	if job.Attempts >= maxAttempts {
		job.Status = "failed"
	} else {
		job.Status = "retrying"
	}
	job.LastError = lastErr
	return nil
}

func (f *fakeJobRepository) jobStatus(jobID string) string { return f.jobs[jobID].Status }

type fakeEmbedder struct {
	metadata ports.EmbeddingMetadata
	failFor  map[string]error
	embedded []string
}

func (f *fakeEmbedder) Metadata() ports.EmbeddingMetadata { return f.metadata }

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err, ok := f.failFor[text]; ok {
		return nil, err
	}
	f.embedded = append(f.embedded, text)
	vector := make([]float32, f.metadata.Dimensions)
	vector[0] = 1
	return vector, nil
}

type fakeEmbeddingRepository struct {
	saveErrFor map[string]error
	saved      map[string][]float32
}

func (f *fakeEmbeddingRepository) SaveEmbedding(ctx context.Context, tenantID, memoryID, modelID, artifact, version string, vector []float32) error {
	if err, ok := f.saveErrFor[memoryID]; ok {
		return err
	}
	if f.saved == nil {
		f.saved = map[string][]float32{}
	}
	f.saved[memoryID] = vector
	return nil
}

func (f *fakeEmbeddingRepository) SearchSimilar(ctx context.Context, tenantID, userID, sessionID, modelID, artifact, version string, vector []float32, limit int) ([]ports.VectorSearchResult, error) {
	return nil, nil
}

func TestWorkerProcessBatchEmbedsAndCompletes(t *testing.T) {
	jobs := newFakeJobRepository()
	embedder := &fakeEmbedder{metadata: ports.EmbeddingMetadata{ModelID: "all-MiniLM-L6-v2", Artifact: "all-MiniLM-L6-v2-Q8_0.gguf", Version: "sha-1", Dimensions: 4}}
	store := &fakeEmbeddingRepository{}
	ctx := context.Background()
	for _, memory := range []string{"m1", "m2"} {
		if err := jobs.EnqueueEmbeddingJob(ctx, "tenant", memory, embedder.metadata.ModelID, embedder.metadata.Artifact, embedder.metadata.Version, "embed:"+memory+":sha-1"); err != nil {
			t.Fatal(err)
		}
	}
	worker := NewWorker(jobs, store, embedder, 10, 3)
	processed, err := worker.ProcessBatch(ctx, "tenant")
	if err != nil || processed != 2 {
		t.Fatalf("processed=%d err=%v, want 2 processed without error", processed, err)
	}
	if len(jobs.completed) != 2 || jobs.jobStatus("A") != "succeeded" || jobs.jobStatus("B") != "succeeded" {
		t.Fatalf("jobs not completed: %v %q %q", jobs.completed, jobs.jobStatus("A"), jobs.jobStatus("B"))
	}
	if len(store.saved) != 2 {
		t.Fatalf("saved embeddings = %d, want 2", len(store.saved))
	}
}

func TestWorkerRetriesTransientFailuresUntilDeadLetter(t *testing.T) {
	jobs := newFakeJobRepository()
	embedder := &fakeEmbedder{
		metadata: ports.EmbeddingMetadata{ModelID: "all-MiniLM-L6-v2", Artifact: "all-MiniLM-L6-v2-Q8_0.gguf", Version: "sha-1", Dimensions: 4},
		failFor:  map[string]error{"content of m1": errors.New("llama exploded")},
	}
	store := &fakeEmbeddingRepository{}
	ctx := context.Background()
	if err := jobs.EnqueueEmbeddingJob(ctx, "tenant", "m1", embedder.metadata.ModelID, embedder.metadata.Artifact, embedder.metadata.Version, "embed:m1:sha-1"); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(jobs, store, embedder, 10, 2)
	for round := 1; round <= 3; round++ {
		processed, err := worker.ProcessBatch(ctx, "tenant")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if processed != 0 {
			t.Fatalf("round %d processed=%d, want 0", round, processed)
		}
	}
	if status := jobs.jobStatus("A"); status != "failed" {
		t.Fatalf("job status = %q, want failed after exhausting retries", status)
	}
	if len(jobs.retryCalls) != 2 || jobs.retryCalls[1].maxTries != 2 {
		t.Fatalf("retry calls = %#v, want two retries capped at 2 attempts", jobs.retryCalls)
	}
	if len(store.saved) != 0 {
		t.Fatal("failed embedding produced a saved vector")
	}
}

func TestWorkerMarksSaveFailureRetryableAndSkipsIdentityMismatch(t *testing.T) {
	jobs := newFakeJobRepository()
	embedder := &fakeEmbedder{metadata: ports.EmbeddingMetadata{ModelID: "all-MiniLM-L6-v2", Artifact: "all-MiniLM-L6-v2-Q8_0.gguf", Version: "sha-2", Dimensions: 4}}
	store := &fakeEmbeddingRepository{saveErrFor: map[string]error{"m1": errors.New("vector write rejected")}}
	ctx := context.Background()
	// m1 job targets the current identity but its save fails; m2 job carries an
	// outdated identity and must complete without embedding.
	if err := jobs.EnqueueEmbeddingJob(ctx, "tenant", "m1", embedder.metadata.ModelID, embedder.metadata.Artifact, "sha-2", "embed:m1:sha-2"); err != nil {
		t.Fatal(err)
	}
	if err := jobs.EnqueueEmbeddingJob(ctx, "tenant", "m2", embedder.metadata.ModelID, embedder.metadata.Artifact, "sha-old", "embed:m2:sha-old"); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(jobs, store, embedder, 10, 3)
	processed, err := worker.ProcessBatch(ctx, "tenant")
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed=%d, want 0", processed)
	}
	if status := jobs.jobStatus("B"); status != "succeeded" {
		t.Fatalf("mismatched identity job status = %q, want completed without embedding", status)
	}
	if status := jobs.jobStatus("A"); status != "retrying" {
		t.Fatalf("save-failed job status = %q, want retrying", status)
	}
	if strings.Join(embedder.embedded, ",") != "content of m1" {
		t.Fatalf("embedded texts = %v, want only the current-identity job", embedder.embedded)
	}
}

func TestWorkerRebuildForModelDelegatesCurrentEmbedderIdentity(t *testing.T) {
	jobs := newFakeJobRepository()
	embedder := &fakeEmbedder{metadata: ports.EmbeddingMetadata{ModelID: "all-MiniLM-L6-v2", Artifact: "all-MiniLM-L6-v2-Q8_0.gguf", Version: "sha-9", Dimensions: 4}}
	worker := NewWorker(jobs, &fakeEmbeddingRepository{}, embedder, 10, 3)
	if _, err := worker.RebuildForModel(context.Background(), "tenant"); err != nil {
		t.Fatalf("RebuildForModel: %v", err)
	}
	if jobs.rebuildTarget.ModelID != embedder.metadata.ModelID || jobs.rebuildTarget.Artifact != embedder.metadata.Artifact || jobs.rebuildTarget.Version != embedder.metadata.Version {
		t.Fatalf("rebuild target = %#v, want current embedder identity", jobs.rebuildTarget)
	}
}

func TestWorkerRejectsEmptyTenant(t *testing.T) {
	worker := NewWorker(newFakeJobRepository(), &fakeEmbeddingRepository{}, &fakeEmbedder{metadata: ports.EmbeddingMetadata{Dimensions: 4}}, 10, 3)
	if _, err := worker.ProcessBatch(context.Background(), ""); err == nil {
		t.Fatal("ProcessBatch accepted an empty tenant id")
	}
}
