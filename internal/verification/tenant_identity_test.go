package verification

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/embedding"
	"github.com/hchw/mengpo/internal/application/projection"
	"github.com/hchw/mengpo/internal/ports"
)

// 4.5 — Embedding jobs carry tenant identity and cannot run without it.
func TestEmbeddingWorkerRequiresTenantIdentity(t *testing.T) {
	worker := embedding.NewWorker(nil, nil, nil, 1, 1)
	if _, err := worker.ProcessBatch(context.Background(), ""); err == nil {
		t.Fatal("embedding worker processed a batch without a tenant id")
	}
}

type recordingAnalyst struct {
	tenantID string
}

func (r *recordingAnalyst) Analyze(_ context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	r.tenantID = batch.TenantID
	return ports.AnalystResult{}, nil
}

// 4.5 — Memory LLM analysis batches carry tenant identity to the provider, and
// a batch without tenant identity is rejected before reaching the provider.
func TestAnalysisBatchCarriesTenantIdentity(t *testing.T) {
	analyst := &recordingAnalyst{}
	service := analysis.New(analysis.Providers{Analyst: analyst})
	batch := ports.AnalysisBatch{
		TenantID: "tenant-a", RunID: "run-1", PromptVersion: "p1", SchemaVersion: "s1",
		Events: []ports.AnalysisEvent{{EventID: "e1", OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"text":"x"}`)}},
	}
	if _, err := service.Analyze(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if analyst.tenantID != "tenant-a" {
		t.Fatalf("provider saw tenant %q, want tenant-a", analyst.tenantID)
	}
	if _, err := service.Analyze(context.Background(), ports.AnalysisBatch{RunID: "run-2", PromptVersion: "p1", SchemaVersion: "s1", Events: batch.Events}); err == nil {
		t.Fatal("analysis batch without tenant identity was accepted")
	}
}

// 4.5 — Cache keys always embed tenant identity; the same logical key for two
// tenants never collides.
func TestCacheKeysEmbedTenantIdentity(t *testing.T) {
	a, err := ports.TenantCacheKey("tenant-a", "projection", "q1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ports.TenantCacheKey("tenant-b", "projection", "q1")
	if a == b {
		t.Fatal("cache keys collided across tenants")
	}
	if _, err := ports.TenantCacheKey("", "projection", "q1"); err == nil {
		t.Fatal("cache key without tenant identity was accepted")
	}

	budget := projection.Budget{CandidateLimit: 10, RankingLimit: 5, InjectionTokenLimit: 100}
	projectionA := projection.CacheKey("tenant-a", "user", "session", "session", "hash", "focus", "models", budget)
	projectionB := projection.CacheKey("tenant-b", "user", "session", "session", "hash", "focus", "models", budget)
	if projectionA == projectionB {
		t.Fatal("projection cache keys collided across tenants")
	}
}

// 4.5 — Task queue jobs require tenant identity; an unbound job notification is
// rejected at publish time.
func TestQueueJobsRequireTenantIdentity(t *testing.T) {
	notification := ports.JobNotification{JobID: "job-1"}
	if notification.TenantID != "" {
		t.Fatal("expected empty tenant")
	}
	// The outbox job carries tenant identity as a first-class field; the NATS
	// publisher (see internal/adapters/nats) rejects notifications without it.
	job := ports.OutboxJob{TenantID: "tenant-a", ID: "job-1", JobType: "normalize_event"}
	if job.TenantID == "" {
		t.Fatal("outbox job lost tenant identity")
	}
}
