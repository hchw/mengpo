package assembly

import (
	"context"

	"github.com/hchw/mengpo/internal/workers"
)

// outboxPollJob drives the durable queue one lease/handle pass per tenant. The
// PubSub wake-up path still triggers an immediate pass, so latency does not
// regress when the blocking poller is replaced by a scheduled plan.
type outboxPollJob struct {
	name   string
	runner *workers.OutboxRunner
}

func (j outboxPollJob) Name() string { return j.name }

func (j outboxPollJob) Run(ctx context.Context, tenantID string) error {
	return j.runner.PollTenant(ctx, tenantID)
}

// embeddingPlan drains one tenant's embedding jobs per trigger.
type embeddingPlan struct {
	name   string
	runner EmbeddingRunner
}

func (j embeddingPlan) Name() string { return j.name }

func (j embeddingPlan) Run(ctx context.Context, tenantID string) error {
	_, err := j.runner.ProcessBatch(ctx, tenantID)
	return err
}
