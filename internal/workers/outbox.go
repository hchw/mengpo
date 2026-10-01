package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type JobHandler func(context.Context, ports.OutboxJob) error

type OutboxRunner struct {
	Repository   ports.OutboxRepository
	Notifier     ports.PubSub
	Tenants      []string
	WorkerID     string
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
	RetryDelay   time.Duration
	Handle       JobHandler
	Now          func() time.Time
}

// Run always performs periodic PostgreSQL polling. Pub/Sub notifications merely
// shorten latency; absent, replaced, or unavailable MQ adapters do not affect recovery.
func (r *OutboxRunner) Run(ctx context.Context) error {
	if r == nil || r.Repository == nil || r.WorkerID == "" || len(r.Tenants) == 0 || r.Handle == nil {
		return fmt.Errorf("outbox runner is not configured")
	}
	batch, lease, poll, retryDelay := r.BatchSize, r.Lease, r.PollInterval, r.RetryDelay
	if batch <= 0 {
		batch = 16
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	if poll <= 0 {
		poll = time.Second
	}
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	var wake <-chan ports.JobNotification
	if r.Notifier != nil {
		wake, _ = r.Notifier.Subscribe(ctx)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if err := r.poll(ctx, batch, lease, retryDelay, now); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-wake:
			if !ok {
				wake = nil
			}
		case <-ticker.C:
		}
	}
}

func (r *OutboxRunner) poll(ctx context.Context, batch int, lease, retryDelay time.Duration, now func() time.Time) error {
	for _, tenantID := range r.Tenants {
		jobs, err := r.Repository.LeaseJobs(ctx, tenantID, r.WorkerID, batch, lease)
		if err != nil {
			return fmt.Errorf("lease jobs for tenant %s: %w", tenantID, err)
		}
		for _, job := range jobs {
			if err := r.Repository.StartJob(ctx, tenantID, job.ID, r.WorkerID); err != nil {
				return fmt.Errorf("start job %s: %w", job.ID, err)
			}
			if err := r.Handle(ctx, job); err != nil {
				if retryErr := r.Repository.RetryJob(ctx, tenantID, job.ID, r.WorkerID, err.Error(), now().Add(retryDelay)); retryErr != nil {
					return fmt.Errorf("retry job %s: %w", job.ID, retryErr)
				}
				continue
			}
			if err := r.Repository.CompleteJob(ctx, tenantID, job.ID, r.WorkerID); err != nil {
				return fmt.Errorf("complete job %s: %w", job.ID, err)
			}
		}
	}
	return nil
}
