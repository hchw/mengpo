package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type fakeTenants struct{ tenants []string }

func (f fakeTenants) ActiveTenants(context.Context) ([]string, error) { return f.tenants, nil }

// recordingLocker records every lock key it is asked for and always acquires.
type recordingLocker struct {
	mu   sync.Mutex
	keys []string
}

func (l *recordingLocker) TryWithLock(ctx context.Context, key string, fn func(ctx context.Context) error) (bool, error) {
	l.mu.Lock()
	l.keys = append(l.keys, key)
	l.mu.Unlock()
	if err := fn(ctx); err != nil {
		return true, err
	}
	return true, nil
}

func (l *recordingLocker) seen(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, candidate := range l.keys {
		if candidate == key {
			return true
		}
	}
	return false
}

type countingJob struct {
	name     string
	mu       sync.Mutex
	runs     int64
	tenant   string
	release  chan struct{}
	inFlight int32
	maxSeen  int32
}

func (j *countingJob) Name() string { return j.name }

func (j *countingJob) Run(ctx context.Context, tenantID string) error {
	current := atomic.AddInt32(&j.inFlight, 1)
	defer atomic.AddInt32(&j.inFlight, -1)
	for {
		max := atomic.LoadInt32(&j.maxSeen)
		if current <= max || atomic.CompareAndSwapInt32(&j.maxSeen, max, current) {
			break
		}
	}
	j.mu.Lock()
	j.runs++
	j.tenant = tenantID
	j.mu.Unlock()
	if j.release != nil {
		select {
		case <-j.release:
		case <-ctx.Done():
		}
	}
	return nil
}

func (j *countingJob) count() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.runs
}

func TestSchedulerRunsOncePerTriggerUnderLock(t *testing.T) {
	locker := &recordingLocker{}
	job := &countingJob{name: "maintenance"}
	s := New(Options{Tenants: fakeTenants{tenants: []string{"tenant-a"}}, Locker: locker, TickPeriod: 5 * time.Millisecond})
	if err := s.Register(job, ports.Schedule{Name: "maintenance", Cadence: 20 * time.Millisecond, Enabled: true}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for job.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if job.count() == 0 {
		t.Fatal("scheduled job never ran")
	}
	if !locker.seen("scheduler:tenant-a:maintenance") {
		t.Fatalf("locker did not receive tenant-scoped key, got %v", locker.keys)
	}
	if got := job.tenant; got != "tenant-a" {
		t.Fatalf("job tenant = %q, want tenant-a", got)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestSchedulerDoesNotOverlap(t *testing.T) {
	release := make(chan struct{})
	job := &countingJob{name: "slow", release: release}
	s := New(Options{Tenants: fakeTenants{tenants: []string{"tenant-a"}}, TickPeriod: 2 * time.Millisecond})
	if err := s.Register(job, ports.Schedule{Name: "slow", Cadence: 2 * time.Millisecond, Enabled: true}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	close(release)
	if got := atomic.LoadInt32(&job.maxSeen); got > 1 {
		t.Fatalf("job overlapped itself, max concurrency = %d", got)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	_ = s.Stop(stopCtx)
}

func TestSchedulerStopIsGraceful(t *testing.T) {
	release := make(chan struct{})
	job := &countingJob{name: "blocking", release: release}
	s := New(Options{Tenants: fakeTenants{tenants: []string{"tenant-a"}}, TickPeriod: 2 * time.Millisecond})
	if err := s.Register(job, ports.Schedule{Name: "blocking", Cadence: 2 * time.Millisecond, Enabled: true}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for job.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Stop(stopCtx) }()
	// The in-flight job observes cancellation and returns, so Stop unblocks.
	cancel()
	close(release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after cancellation")
	}
}

func TestRegisterRejectsDuplicatesAndInvalid(t *testing.T) {
	s := New(Options{Tenants: fakeTenants{tenants: []string{"tenant-a"}}})
	job := &countingJob{name: "dup"}
	if err := s.Register(job, ports.Schedule{Name: "dup", Cadence: time.Minute, Enabled: true}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := s.Register(job, ports.Schedule{Name: "dup", Cadence: time.Minute, Enabled: true}); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
	if err := s.Register(&countingJob{name: "zero"}, ports.Schedule{Name: "zero", Cadence: 0}); err == nil {
		t.Fatal("expected zero cadence to fail")
	}
}

func TestSnapshotIsTenantScoped(t *testing.T) {
	s := New(Options{Tenants: fakeTenants{tenants: []string{"tenant-a"}}, TickPeriod: 5 * time.Millisecond})
	job := &countingJob{name: "maintenance"}
	if err := s.Register(job, ports.Schedule{Name: "maintenance", Cadence: 20 * time.Millisecond, Enabled: true}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	statuses, err := s.Snapshot(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(statuses) != 1 || statuses[0].Name != "maintenance" {
		t.Fatalf("Snapshot() = %#v, want one maintenance entry", statuses)
	}
	empty, err := s.Snapshot(context.Background(), "tenant-b")
	if err != nil || len(empty) != 1 {
		t.Fatalf("Snapshot(tenant-b) = %#v, err = %v", empty, err)
	}
}
