// Package scheduler provides the built-in, replaceable periodic scheduler. It
// is a thin, dependency-free implementation that owns no domain logic: it walks
// the active tenants, honours each tenant's own schedule, and relies on a
// cross-process lock so multiple replicas never double-run a trigger.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

// TenantSource returns the tenants the scheduler should consider. It is
// refreshed on every tick so provisioning and suspension need no restart.
type TenantSource interface {
	ActiveTenants(ctx context.Context) ([]string, error)
}

// Locker runs fn while holding a cross-process lock for key. It reports whether
// the lock was acquired; when false, fn is not run (another replica won).
type Locker = ports.Locker

// ScheduleStore persists per-tenant cadence/enabled overrides. When nil the
// registered defaults are used for every tenant.
type ScheduleStore interface {
	Load(ctx context.Context, tenantID, name string) (ports.Schedule, bool, error)
}

// ScheduleEnsurer seeds a tenant's schedule row with the registered defaults so
// the plan is visible before its first run. It is optional.
type ScheduleEnsurer interface {
	Ensure(ctx context.Context, tenantID, name string, cadence time.Duration, enabled bool) error
}

// StatusRecorder persists a schedule's last outcome so an out-of-process
// console can display it. It is optional.
type StatusRecorder interface {
	RecordStatus(ctx context.Context, tenantID, name, status, errMessage string, duration time.Duration, at time.Time) error
}

// ScheduleMetrics records schedule executions so their volume and failures are
// observable per tenant and schedule.
type ScheduleMetrics interface {
	RecordScheduleRun(tenantID, schedule, status string, duration time.Duration)
}

// Options configure the built-in scheduler.
type Options struct {
	Tenants    TenantSource
	Locker     Locker
	Overrides  ScheduleStore
	Metrics    ScheduleMetrics
	Logger     *slog.Logger
	Clock      func() time.Time
	TickPeriod time.Duration
}

// Scheduler is the built-in in-process scheduler.
type Scheduler struct {
	options Options

	mu      sync.Mutex
	entries map[string]*entry
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type entry struct {
	job      ports.ScheduledJob
	defaults ports.Schedule
	tenants  map[string]*tenantState
}

type tenantState struct {
	tenantID string
	cadence  time.Duration
	enabled  bool
	running  bool
	nextRun  time.Time
	status   ports.ScheduleStatus
}

// New builds the built-in scheduler.
func New(options Options) *Scheduler {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.TickPeriod <= 0 {
		options.TickPeriod = 5 * time.Second
	}
	if options.Locker == nil {
		options.Locker = localLocker{}
	}
	return &Scheduler{options: options, entries: map[string]*entry{}}
}

// localLocker is the single-process default: it always runs the trigger. Use
// the PostgreSQL advisory locker for multi-replica deployments.
type localLocker struct{}

func (localLocker) TryWithLock(ctx context.Context, _ string, fn func(ctx context.Context) error) (bool, error) {
	if err := fn(ctx); err != nil {
		return true, err
	}
	return true, nil
}

var (
	ErrSchedulerNotConfigured = errors.New("scheduler requires a tenant source")
	ErrJobAlreadyRegistered   = errors.New("schedule job already registered")
	ErrInvalidSchedule        = errors.New("invalid schedule definition")
)

func (s *Scheduler) Register(job ports.ScheduledJob, defaults ports.Schedule) error {
	if job == nil || job.Name() == "" {
		return ErrInvalidSchedule
	}
	if defaults.Name == "" {
		defaults.Name = job.Name()
	}
	if defaults.Name != job.Name() || defaults.Cadence <= 0 {
		return ErrInvalidSchedule
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[job.Name()]; exists {
		return fmt.Errorf("%w: %s", ErrJobAlreadyRegistered, job.Name())
	}
	s.entries[job.Name()] = &entry{job: job, defaults: defaults, tenants: map[string]*tenantState{}}
	return nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	if s.options.Tenants == nil {
		return ErrSchedulerNotConfigured
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.options.Logger != nil {
		s.options.Logger.Info("scheduler started", slog.Int("plans", len(s.entries)))
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(runCtx)
	}()
	return nil
}

func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.started = false
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) Snapshot(_ context.Context, tenantID string) ([]ports.ScheduleStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []ports.ScheduleStatus
	for _, e := range s.entries {
		state, ok := e.tenants[tenantID]
		if !ok {
			// Tenant has never been observed; report the defaults as pending.
			result = append(result, ports.ScheduleStatus{Name: e.defaults.Name, Cadence: e.defaults.Cadence})
			continue
		}
		status := state.status
		status.Name = e.job.Name()
		status.Cadence = state.cadence
		status.NextRun = state.nextRun
		result = append(result, status)
	}
	return result, nil
}

func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(s.options.TickPeriod)
	defer ticker.Stop()
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	tenants, err := s.options.Tenants.ActiveTenants(ctx)
	if err != nil {
		if s.options.Logger != nil {
			s.options.Logger.Error("scheduler tenant refresh failed", slog.String("error", err.Error()))
		}
		return
	}
	now := s.options.Clock()
	s.mu.Lock()
	type pending struct {
		state *tenantState
		job   ports.ScheduledJob
	}
	var due []pending
	for _, e := range s.entries {
		for _, tenantID := range tenants {
			state := s.stateFor(ctx, e, tenantID, now)
			if state == nil || !state.enabled || state.running || now.Before(state.nextRun) {
				continue
			}
			state.running = true
			state.nextRun = now.Add(state.cadence)
			due = append(due, pending{state: state, job: e.job})
		}
	}
	s.mu.Unlock()
	for _, item := range due {
		s.run(ctx, item.state, item.job)
	}
}

// stateFor returns (creating if needed) the per-tenant state, applying any
// override the tenant has persisted for this schedule.
func (s *Scheduler) stateFor(ctx context.Context, e *entry, tenantID string, now time.Time) *tenantState {
	if state, ok := e.tenants[tenantID]; ok {
		return state
	}
	schedule := e.defaults
	if s.options.Overrides != nil {
		if override, ok, err := s.options.Overrides.Load(ctx, tenantID, e.job.Name()); err == nil && ok {
			schedule.Cadence = override.Cadence
			schedule.Enabled = override.Enabled
		}
	}
	if ensurer, ok := s.options.Overrides.(ScheduleEnsurer); ok && ensurer != nil {
		if err := ensurer.Ensure(ctx, tenantID, e.job.Name(), schedule.Cadence, schedule.Enabled); err != nil && s.options.Logger != nil {
			s.options.Logger.Warn("seed tenant schedule failed", slog.String("tenant", tenantID), slog.String("schedule", e.job.Name()), slog.String("error", err.Error()))
		}
	}
	state := &tenantState{tenantID: tenantID, cadence: schedule.Cadence, enabled: schedule.Enabled, nextRun: now.Add(schedule.Cadence)}
	state.status = ports.ScheduleStatus{Name: e.job.Name(), Cadence: schedule.Cadence, NextRun: state.nextRun, LastStatus: "pending"}
	e.tenants[tenantID] = state
	return state
}

func (s *Scheduler) run(ctx context.Context, state *tenantState, job ports.ScheduledJob) {
	tenantID := state.tenantID
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			state.running = false
			s.mu.Unlock()
		}()
		started := s.options.Clock()
		executed, err := s.options.Locker.TryWithLock(ctx, lockKey(tenantID, job.Name()), func(runCtx context.Context) error {
			return job.Run(runCtx, tenantID)
		})
		duration := s.options.Clock().Sub(started)
		s.mu.Lock()
		defer s.mu.Unlock()
		if !executed {
			// Another replica owns this trigger; leave the prior status intact.
			return
		}
		state.status.LastRun = started
		state.status.LastDuration = duration
		state.status.Runs++
		if err != nil {
			state.status.LastStatus = "error"
			state.status.LastError = err.Error()
			s.recordError(tenantID, job.Name(), err.Error(), duration)
			if s.options.Logger != nil {
				s.options.Logger.Error("scheduled job failed",
					slog.String("tenant", tenantID), slog.String("schedule", job.Name()), slog.String("error", err.Error()))
			}
			return
		}
		state.status.LastStatus = "ok"
		state.status.LastError = ""
		s.record(tenantID, job.Name(), "ok", duration)
	}()
}

func (s *Scheduler) recordError(tenantID, name, message string, duration time.Duration) {
	if s.options.Metrics != nil {
		s.options.Metrics.RecordScheduleRun(tenantID, name, "error", duration)
	}
	if recorder, ok := s.options.Overrides.(StatusRecorder); ok && recorder != nil {
		_ = recorder.RecordStatus(context.WithoutCancel(context.Background()), tenantID, name, "error", message, duration, s.options.Clock())
	}
}

func (s *Scheduler) record(tenantID, name, status string, duration time.Duration) {
	if s.options.Metrics != nil {
		s.options.Metrics.RecordScheduleRun(tenantID, name, status, duration)
	}
	if recorder, ok := s.options.Overrides.(StatusRecorder); ok && recorder != nil {
		message := ""
		if status != "ok" {
			message = status
		}
		_ = recorder.RecordStatus(context.WithoutCancel(context.Background()), tenantID, name, status, message, duration, s.options.Clock())
	}
}

func lockKey(tenantID, name string) string {
	return "scheduler:" + tenantID + ":" + name
}

var _ ports.Scheduler = (*Scheduler)(nil)
