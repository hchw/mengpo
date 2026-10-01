package ports

import (
	"context"
	"time"
)

// Schedule declares a periodic maintenance job's default cadence. Each tenant
// owns its own schedules; defaults are overridable per tenant (see the tenant
// schedule store).
type Schedule struct {
	Name    string
	Cadence time.Duration
	Enabled bool
}

// ScheduledJob is a unit of periodic work. Run is always scoped to a single
// tenant so a job can never leak work across tenants.
type ScheduledJob interface {
	Name() string
	Run(ctx context.Context, tenantID string) error
}

// ScheduleStatus is the observable state of one (tenant, schedule) pair. It is
// surfaced through the owning capability's interface, not a standalone page.
type ScheduleStatus struct {
	Name         string
	Cadence      time.Duration
	NextRun      time.Time
	LastRun      time.Time
	LastStatus   string
	LastDuration time.Duration
	LastError    string
	Runs         int64
}

// Scheduler runs registered jobs periodically, per tenant. Implementations must
// guarantee that a single trigger of one (tenant, schedule) executes at most
// once across replicas and never overlaps itself.
type Scheduler interface {
	Register(job ScheduledJob, defaults Schedule) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Snapshot(ctx context.Context, tenantID string) ([]ScheduleStatus, error)
}

// Locker runs fn while holding a cross-process lock for key. It reports whether
// the lock was acquired; when false, fn is not run (another replica won).
type Locker interface {
	TryWithLock(ctx context.Context, key string, fn func(ctx context.Context) error) (bool, error)
}
