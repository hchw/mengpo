package assembly

import (
	"context"
	"database/sql"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMigrateIsIdempotent proves the startup migration is safe to run on every
// process start and on an already-migrated database.
func TestMigrateIsIdempotent(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second migration must be idempotent: %v", err)
	}
}

type fakeTenants struct{ ids []string }

func (f fakeTenants) ActiveTenants(context.Context) ([]string, error) { return f.ids, nil }

type fakeOutbox struct{}

func (fakeOutbox) LeaseJobs(context.Context, string, string, int, time.Duration) ([]ports.OutboxJob, error) {
	return nil, nil
}
func (fakeOutbox) StartJob(context.Context, string, string, string) error    { return nil }
func (fakeOutbox) CompleteJob(context.Context, string, string, string) error { return nil }
func (fakeOutbox) RetryJob(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (fakeOutbox) CancelJob(context.Context, string, string) error        { return nil }
func (fakeOutbox) ReplayDeadLetter(context.Context, string, string) error { return nil }

type countingEmbedding struct{ calls atomic.Int32 }

func (c *countingEmbedding) ProcessBatch(context.Context, string) (int, error) {
	c.calls.Add(1)
	return 0, nil
}

func (c *countingEmbedding) RebuildForModel(context.Context, string) (int, error) {
	return 0, nil
}

// TestWorkerRunsAndStops verifies the worker starts its embedding loop, keeps
// polling the durable outbox, and shuts down cleanly when the context is
// cancelled.
func TestWorkerRunsAndStops(t *testing.T) {
	embedding := &countingEmbedding{}
	worker, err := NewWorkerWithOptions(WorkerOptions{
		DB:              &sql.DB{},
		Tenants:         fakeTenants{ids: []string{"tenant-1"}},
		Outbox:          fakeOutbox{},
		Embedding:       embedding,
		PollInterval:    5 * time.Millisecond,
		EmbeddingPeriod: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorkerWithOptions() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for embedding.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("embedding loop never ran")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

// TestWorkerRejectsIncompleteOptions keeps misconfiguration a startup error.
func TestWorkerRejectsIncompleteOptions(t *testing.T) {
	if _, err := NewWorkerWithOptions(WorkerOptions{}); err == nil {
		t.Fatal("NewWorkerWithOptions() accepted missing database")
	}
}
