package ports

import (
	"context"
	"encoding/json"
	"time"
)

type OutboxJob struct {
	ID             string
	TenantID       string
	JobType        string
	AggregateID    string
	IdempotencyKey string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	MaxAttempts    int
	LeaseOwner     string
	LeaseUntil     time.Time
}

type OutboxRepository interface {
	LeaseJobs(ctx context.Context, tenantID, workerID string, limit int, lease time.Duration) ([]OutboxJob, error)
	StartJob(ctx context.Context, tenantID, jobID, workerID string) error
	CompleteJob(ctx context.Context, tenantID, jobID, workerID string) error
	RetryJob(ctx context.Context, tenantID, jobID, workerID, failure string, retryAt time.Time) error
	CancelJob(ctx context.Context, tenantID, jobID string) error
	ReplayDeadLetter(ctx context.Context, tenantID, jobID string) error
}

// OutboxEnqueuer persists a new durable job. Jobs are the reliability boundary:
// enqueueing is a plain tenant-scoped insert and is independent of any MQ
// notification, so callers can schedule work without a broker.
type OutboxEnqueuer interface {
	EnqueueJob(ctx context.Context, job OutboxJob) error
}
