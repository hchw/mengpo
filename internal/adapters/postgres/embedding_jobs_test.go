package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestEmbeddingJobVersionSwitchAndRebuild covers task 6.2: model version
// switch marks old embeddings stale, enqueues idempotent rebuild jobs, jobs
// are claimed atomically, and retries transition to failed with the memory
// embedding_status set to failed.
func TestEmbeddingJobVersionSwitchAndRebuild(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL/pgvector integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "embedding jobs tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	memories := NewMemoryRepository(router)
	embeddings := NewEmbeddingRepository(router)

	userID := "00000000-0000-4000-8000-000000000021"
	memoryIDs := []string{
		"00000000-0000-4000-8000-000000000521",
		"00000000-0000-4000-8000-000000000522",
		"00000000-0000-4000-8000-000000000523",
	}
	for index, memoryID := range memoryIDs {
		if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
			ID:               memoryID,
			IdempotencyKey:   fmt.Sprintf("jobs-fixture-%d", index),
			UserID:           userID,
			ScopeType:        "user-global",
			ScopeID:          userID,
			MemoryType:       "fact",
			Status:           "stable",
			Confidence:       0.8,
			Applicability:    json.RawMessage(`{}`),
			Content:          json.RawMessage(fmt.Sprintf(`{"text":"fixture %d"}`, index)),
			ContentText:      fmt.Sprintf("fixture content %d", index),
			DefaultRetrieval: true,
		}); err != nil {
			t.Fatalf("create memory fixture: %v", err)
		}
	}

	vector := make([]float32, VectorDimensions)
	vector[0] = 1
	for _, memoryID := range memoryIDs {
		if err := embeddings.SaveEmbedding(ctx, tenant.ID, memoryID, "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "old-sha", vector); err != nil {
			t.Fatalf("SaveEmbedding initial: %v", err)
		}
	}

	// Enqueue must be idempotent per key.
	for i := 0; i < 2; i++ {
		if err := embeddings.EnqueueEmbeddingJob(ctx, tenant.ID, memoryIDs[0], "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "new-sha", "embed:"+memoryIDs[0]+":new-sha"); err != nil {
			t.Fatalf("EnqueueEmbeddingJob: %v", err)
		}
	}
	claim, err := embeddings.ClaimEmbeddingJobs(ctx, tenant.ID, 10)
	if err != nil {
		t.Fatalf("ClaimEmbeddingJobs: %v", err)
	}
	if len(claim) != 1 || claim[0].Attempts != 1 || claim[0].ContentText == "" {
		t.Fatalf("idempotent enqueue produced %d claimable jobs: %#v", len(claim), claim)
	}
	if err := embeddings.RetryEmbeddingJob(ctx, tenant.ID, claim[0].ID, "injected failure", 3); err != nil {
		t.Fatalf("RetryEmbeddingJob: %v", err)
	}
	// Version switch: all three memories carry old-sha, so all become stale and
	// get rebuild jobs under the new identity. The manual job from above is also
	// re-targeted by its matching idempotency key.
	enqueued, err := embeddings.RebuildForModel(ctx, tenant.ID, "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "new-sha")
	if err != nil {
		t.Fatalf("RebuildForModel: %v", err)
	}
	if enqueued != 3 {
		t.Fatalf("RebuildForModel enqueued %d jobs, want 3", enqueued)
	}
	requireEmbeddingStatus := func(memoryID, want string) {
		t.Helper()
		var status string
		if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT embedding_status FROM memory_nodes WHERE id = $1::uuid`, memoryID).Scan(&status)
		}); err != nil {
			t.Fatalf("read embedding_status: %v", err)
		}
		if status != want {
			t.Fatalf("memory %s embedding_status = %q, want %q", memoryID, status, want)
		}
	}
	for _, memoryID := range memoryIDs {
		requireEmbeddingStatus(memoryID, "stale")
	}

	// Exhaust retries deterministically: the oldest job (J0, attempts already 1)
	// is claimed with limit 1 until attempts reach the cap, then it must
	// dead-letter and mark its memory embedding_status failed.
	claim, err = embeddings.ClaimEmbeddingJobs(ctx, tenant.ID, 1)
	if err != nil {
		t.Fatalf("ClaimEmbeddingJobs dead-letter: %v", err)
	}
	if len(claim) != 1 || claim[0].Attempts != 2 {
		t.Fatalf("dead-letter claim = %#v, want one job at attempt 2", claim)
	}
	if err := embeddings.RetryEmbeddingJob(ctx, tenant.ID, claim[0].ID, "injected failure", 3); err != nil {
		t.Fatalf("RetryEmbeddingJob dead-letter: %v", err)
	}
	claim, err = embeddings.ClaimEmbeddingJobs(ctx, tenant.ID, 1)
	if err != nil {
		t.Fatalf("ClaimEmbeddingJobs dead-letter final: %v", err)
	}
	if len(claim) != 1 || claim[0].Attempts != 3 {
		t.Fatalf("final dead-letter claim = %#v, want one job at attempt 3", claim)
	}
	deadJob := claim[0]
	if err := embeddings.RetryEmbeddingJob(ctx, tenant.ID, deadJob.ID, "injected failure", 3); err != nil {
		t.Fatalf("RetryEmbeddingJob final: %v", err)
	}
	requireEmbeddingStatus(deadJob.MemoryID, "failed")
	var jobStatus string
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT status, last_error FROM embedding_jobs WHERE id = $1::uuid`, deadJob.ID).Scan(&jobStatus, new(string))
	}); err != nil {
		t.Fatalf("read dead job status: %v", err)
	}
	if jobStatus != "failed" {
		t.Fatalf("dead job status = %q, want failed", jobStatus)
	}

	// Re-enqueue resets the dead job so the failure is replayable. The other
	// two rebuild jobs are claimed together, embedded, and completed.
	if err := embeddings.EnqueueEmbeddingJob(ctx, tenant.ID, deadJob.MemoryID, "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "new-sha", "embed:"+deadJob.MemoryID+":new-sha"); err != nil {
		t.Fatalf("re-enqueue dead job: %v", err)
	}
	claim, err = embeddings.ClaimEmbeddingJobs(ctx, tenant.ID, 10)
	if err != nil {
		t.Fatalf("ClaimEmbeddingJobs after re-enqueue: %v", err)
	}
	if len(claim) != 3 {
		t.Fatalf("claim after re-enqueue returned %d jobs, want 3", len(claim))
	}
	for _, job := range claim {
		if err := embeddings.SaveEmbedding(ctx, tenant.ID, job.MemoryID, job.ModelID, job.Artifact, job.ModelVersion, vector); err != nil {
			t.Fatalf("SaveEmbedding rebuild: %v", err)
		}
		if err := embeddings.CompleteEmbeddingJob(ctx, tenant.ID, job.ID); err != nil {
			t.Fatalf("CompleteEmbeddingJob: %v", err)
		}
	}
	for _, memoryID := range memoryIDs {
		requireEmbeddingStatus(memoryID, "ready")
	}
	var stillQueued int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM embedding_jobs WHERE status IN ('queued', 'retrying', 'running')`).Scan(&stillQueued)
	}); err != nil {
		t.Fatalf("count remaining jobs: %v", err)
	}
	if stillQueued != 0 {
		t.Fatalf("%d embedding jobs still open after rebuild", stillQueued)
	}
	// The rebuilt embeddings must be searchable under the new version only.
	results, err := embeddings.SearchSimilar(ctx, tenant.ID, userID, "", "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "new-sha", vector, 10)
	if err != nil {
		t.Fatalf("SearchSimilar after rebuild: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("vector search under new version returned %d results, want 3", len(results))
	}
	oldResults, err := embeddings.SearchSimilar(ctx, tenant.ID, userID, "", "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "old-sha", vector, 10)
	if err != nil {
		t.Fatalf("SearchSimilar old version: %v", err)
	}
	if len(oldResults) != 0 {
		t.Fatalf("old version still returned %d results after rebuild", len(oldResults))
	}
}
