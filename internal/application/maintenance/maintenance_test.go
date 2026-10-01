package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type fakeReader struct {
	events []ports.PendingAnalysisEvent
	offset int
	limit  int
}

func (f *fakeReader) ListPendingAnalysisEvents(_ context.Context, _ string, offset, limit int) ([]ports.PendingAnalysisEvent, error) {
	f.offset, f.limit = offset, limit
	if offset >= len(f.events) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.events) {
		end = len(f.events)
	}
	return f.events[offset:end], nil
}

func (f *fakeReader) LoadAnalysisEventsByRawIDs(context.Context, string, []string) ([]ports.PendingAnalysisEvent, error) {
	return nil, nil
}

type fakeEnqueuer struct{ jobs []ports.OutboxJob }

func (f *fakeEnqueuer) EnqueueJob(_ context.Context, job ports.OutboxJob) error {
	f.jobs = append(f.jobs, job)
	return nil
}

type fakeCursors struct {
	values map[string]int64
	saves  int
}

func (f *fakeCursors) LoadMaintenanceCursor(_ context.Context, _, taskType string) (int64, error) {
	if f.values == nil {
		return 0, nil
	}
	return f.values[taskType], nil
}

func (f *fakeCursors) SaveMaintenanceCursor(_ context.Context, _, taskType string, watermark int64) error {
	if f.values == nil {
		f.values = map[string]int64{}
	}
	f.values[taskType] = watermark
	f.saves++
	return nil
}

func pendingEvents(n int) []ports.PendingAnalysisEvent {
	events := make([]ports.PendingAnalysisEvent, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, ports.PendingAnalysisEvent{
			NormalizedID: "norm-" + string(rune('a'+i)),
			RawEventID:   "raw-" + string(rune('a'+i)),
			SessionID:    "session-1",
			OccurredAt:   time.Now().UTC(),
			Payload:      json.RawMessage(`{"text":"x"}`),
		})
	}
	return events
}

func TestJobEnqueuesOneConsolidateForABatch(t *testing.T) {
	reader := &fakeReader{events: pendingEvents(3)}
	enqueuer := &fakeEnqueuer{}
	cursors := &fakeCursors{}
	job := &Job{PlanName: "consolidate", TaskType: TaskTypeConsolidate, BatchSize: 10, Reader: reader, Enqueuer: enqueuer, Cursors: cursors}

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(enqueuer.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(enqueuer.jobs))
	}
	enqueued := enqueuer.jobs[0]
	if enqueued.JobType != JobTypeConsolidate || enqueued.TenantID != "tenant-a" {
		t.Fatalf("enqueued job = %+v", enqueued)
	}
	var payload struct {
		RawEventIDs []string `json:"raw_event_ids"`
	}
	if err := json.Unmarshal(enqueued.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.RawEventIDs) != 3 {
		t.Fatalf("payload raw ids = %#v", payload.RawEventIDs)
	}
	if cursors.values[TaskTypeConsolidate] != 3 {
		t.Fatalf("cursor = %d, want 3", cursors.values[TaskTypeConsolidate])
	}

	// Replaying the same window yields the same idempotency key.
	replay := &fakeEnqueuer{}
	cursors2 := &fakeCursors{}
	job2 := &Job{PlanName: "consolidate", TaskType: TaskTypeConsolidate, BatchSize: 10, Reader: &fakeReader{events: pendingEvents(3)}, Enqueuer: replay, Cursors: cursors2}
	if err := job2.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("replay Run() error = %v", err)
	}
	if replay.jobs[0].IdempotencyKey != enqueued.IdempotencyKey {
		t.Fatalf("idempotency key changed across replay: %q vs %q", replay.jobs[0].IdempotencyKey, enqueued.IdempotencyKey)
	}
}

func TestJobSkipsEmptyTenantWithoutSideEffects(t *testing.T) {
	reader := &fakeReader{events: nil}
	enqueuer := &fakeEnqueuer{}
	cursors := &fakeCursors{}
	job := &Job{PlanName: "consolidate", TaskType: TaskTypeConsolidate, BatchSize: 10, Reader: reader, Enqueuer: enqueuer, Cursors: cursors}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(enqueuer.jobs) != 0 {
		t.Fatalf("empty tenant enqueued %d jobs, want 0", len(enqueuer.jobs))
	}
	if cursors.saves != 0 {
		t.Fatalf("empty tenant moved cursor %d times, want 0", cursors.saves)
	}
}
