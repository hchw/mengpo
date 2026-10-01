package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type testOutbox struct {
	completed int
	leased    int
}

func (r *testOutbox) LeaseJobs(_ context.Context, tenant, worker string, _ int, _ time.Duration) ([]ports.OutboxJob, error) {
	r.leased++
	if r.leased > 1 {
		return nil, nil
	}
	return []ports.OutboxJob{{ID: "job", TenantID: tenant, LeaseOwner: worker}}, nil
}
func (r *testOutbox) StartJob(context.Context, string, string, string) error { return nil }
func (r *testOutbox) CompleteJob(context.Context, string, string, string) error {
	r.completed++
	return nil
}
func (*testOutbox) RetryJob(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (*testOutbox) CancelJob(context.Context, string, string) error        { return nil }
func (*testOutbox) ReplayDeadLetter(context.Context, string, string) error { return nil }

type unavailablePubSub struct{}
type replacementPubSub struct{ channel chan ports.JobNotification }

func (p *replacementPubSub) Publish(_ context.Context, n ports.JobNotification) error {
	select {
	case p.channel <- n:
	default:
	}
	return nil
}
func (p *replacementPubSub) Subscribe(context.Context) (<-chan ports.JobNotification, error) {
	return p.channel, nil
}

func (unavailablePubSub) Publish(context.Context, ports.JobNotification) error {
	return errors.New("mq unavailable")
}
func (unavailablePubSub) Subscribe(context.Context) (<-chan ports.JobNotification, error) {
	return nil, errors.New("mq unavailable")
}

func TestOutboxRunnerPollsWhenMQUnavailable(t *testing.T) {
	repo := &testOutbox{}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &OutboxRunner{Repository: repo, Notifier: unavailablePubSub{}, Tenants: []string{"tenant"}, WorkerID: "worker", Handle: func(context.Context, ports.OutboxJob) error { cancel(); return nil }}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if repo.completed != 1 {
		t.Fatalf("completed = %d, want 1", repo.completed)
	}
}

func TestOutboxRunnerWorksWithReplacementPubSub(t *testing.T) {
	repo := &testOutbox{}
	ctx, cancel := context.WithCancel(context.Background())
	pubsub := &replacementPubSub{channel: make(chan ports.JobNotification, 1)}
	runner := &OutboxRunner{Repository: repo, Notifier: pubsub, Tenants: []string{"tenant"}, WorkerID: "worker", Handle: func(context.Context, ports.OutboxJob) error { cancel(); return nil }}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v", err)
	}
	if repo.completed != 1 {
		t.Fatalf("completed=%d,want 1", repo.completed)
	}
}

func TestOutboxRunnerWorksWithoutMQAdapter(t *testing.T) {
	repo := &testOutbox{}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &OutboxRunner{Repository: repo, Tenants: []string{"tenant"}, WorkerID: "worker", Handle: func(context.Context, ports.OutboxJob) error { cancel(); return nil }}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if repo.completed != 1 {
		t.Fatalf("completed = %d, want 1", repo.completed)
	}
}
