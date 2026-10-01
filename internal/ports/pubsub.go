package ports

import "context"

// JobNotification is only a wake-up hint; PostgreSQL remains the durable source of truth.
type JobNotification struct {
	TenantID string `json:"tenant_id"`
	JobID    string `json:"job_id"`
}

type PubSub interface {
	Publish(ctx context.Context, notification JobNotification) error
	Subscribe(ctx context.Context) (<-chan JobNotification, error)
}
