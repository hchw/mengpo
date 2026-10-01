package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type recordingStore struct {
	mu        sync.Mutex
	ensured   []string
	overrides map[string]ports.Schedule
}

func (s *recordingStore) Load(_ context.Context, tenantID, name string) (ports.Schedule, bool, error) {
	if s.overrides == nil {
		return ports.Schedule{}, false, nil
	}
	schedule, ok := s.overrides[tenantID+"|"+name]
	return schedule, ok, nil
}

func (s *recordingStore) Ensure(_ context.Context, tenantID, name string, _ time.Duration, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensured = append(s.ensured, tenantID+"|"+name)
	return nil
}

func (s *recordingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ensured)
}

// TestSchedulerSeedsTenantScheduleRows proves the scheduler persists a tenant's
// schedule row with the registered defaults so the console can show it before
// the first run.
func TestSchedulerSeedsTenantScheduleRows(t *testing.T) {
	store := &recordingStore{}
	s := New(Options{Tenants: fakeTenants{tenants: []string{"tenant-a"}}, Overrides: store, TickPeriod: 5 * time.Millisecond})
	job := &countingJob{name: "consolidate"}
	if err := s.Register(job, ports.Schedule{Name: "consolidate", Cadence: time.Hour, Enabled: true}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for store.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(3 * time.Millisecond)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	_ = s.Stop(stopCtx)
	if store.count() == 0 {
		t.Fatal("scheduler did not seed any tenant schedule row")
	}
	if store.ensured[0] != "tenant-a|consolidate" {
		t.Fatalf("ensured = %#v", store.ensured)
	}
}
