package assembly

import (
	"context"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
	"github.com/hchw/mengpo/internal/workers"
)

// fakeOutboxRepository records leases so the plan wiring can be tested without
// PostgreSQL.
type fakeOutboxRepository struct{ leases int }

func (f *fakeOutboxRepository) LeaseJobs(context.Context, string, string, int, time.Duration) ([]ports.OutboxJob, error) {
	f.leases++
	return nil, nil
}
func (f *fakeOutboxRepository) StartJob(context.Context, string, string, string) error { return nil }
func (f *fakeOutboxRepository) CompleteJob(context.Context, string, string, string) error {
	return nil
}
func (f *fakeOutboxRepository) RetryJob(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (f *fakeOutboxRepository) CancelJob(context.Context, string, string) error { return nil }
func (f *fakeOutboxRepository) ReplayDeadLetter(context.Context, string, string) error {
	return nil
}

// TestOutboxPollPlanDrivesOnePassPerTenant proves the scheduled plan performs a
// single durable-queue pass for the tenant it is given.
func TestOutboxPollPlanDrivesOnePassPerTenant(t *testing.T) {
	repository := &fakeOutboxRepository{}
	runner := &workers.OutboxRunner{
		Repository: repository,
		WorkerID:   "test-worker",
		Handle:     func(context.Context, ports.OutboxJob) error { return nil },
	}
	plan := outboxPollJob{name: "outbox", runner: runner}
	if plan.Name() != "outbox" {
		t.Fatalf("plan name = %q", plan.Name())
	}
	if err := plan.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if repository.leases != 1 {
		t.Fatalf("leases = %d, want 1 (one pass per trigger)", repository.leases)
	}
}
