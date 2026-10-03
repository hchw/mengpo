package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

// TenantScheduleStore reads and writes per-tenant schedule overrides. Each
// tenant owns its own cadence and enabled state, so one tenant's change never
// affects another.
type TenantScheduleStore struct {
	db *sql.DB
}

func NewTenantScheduleStore(db *sql.DB) *TenantScheduleStore {
	return &TenantScheduleStore{db: db}
}

// Load returns the tenant's override for a schedule. The second result is false
// when the tenant has no override and the registered default applies.
func (s *TenantScheduleStore) Load(ctx context.Context, tenantID, name string) (ports.Schedule, bool, error) {
	var cadenceSeconds int
	var enabled bool
	var nextRun sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT cadence_seconds, enabled, next_run_at FROM tenant_schedules WHERE tenant_id = $1 AND name = $2`, tenantID, name).Scan(&cadenceSeconds, &enabled, &nextRun)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Schedule{}, false, nil
	}
	if err != nil {
		return ports.Schedule{}, false, err
	}
	schedule := ports.Schedule{Name: name, Cadence: time.Duration(cadenceSeconds) * time.Second, Enabled: enabled}
	if nextRun.Valid {
		schedule.NextRun = nextRun.Time
	}
	return schedule, true, nil
}

// Ensure seeds a tenant's schedule row with the registered defaults so the
// console can show the plan before its first run. It never overwrites an
// existing row.
func (s *TenantScheduleStore) Ensure(ctx context.Context, tenantID, name string, cadence time.Duration, enabled bool) error {
	if tenantID == "" || name == "" || cadence <= 0 {
		return nil
	}
	seconds := int(cadence / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tenant_schedules (tenant_id, name, cadence_seconds, enabled, next_run_at, updated_at)
VALUES ($1, $2, $3::int, $4, now() + ($3::int * interval '1 second'), now())
ON CONFLICT (tenant_id, name) DO NOTHING`, tenantID, name, seconds, enabled)
	return err
}

// Upsert writes a tenant's override for a schedule.
func (s *TenantScheduleStore) Upsert(ctx context.Context, tenantID, name string, cadence time.Duration, enabled bool) error {
	if tenantID == "" || name == "" || cadence <= 0 {
		return errors.New("tenant schedule requires tenant, name and positive cadence")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tenant_schedules (tenant_id, name, cadence_seconds, enabled, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (tenant_id, name) DO UPDATE SET cadence_seconds = EXCLUDED.cadence_seconds, enabled = EXCLUDED.enabled, updated_at = now()`,
		tenantID, name, int(cadence/time.Second), enabled)
	return err
}

// RecordStatus persists the outcome of one schedule execution so the console
// can show it without reaching into the worker process.
func (s *TenantScheduleStore) RecordStatus(ctx context.Context, tenantID, name, status, errMessage string, _ time.Duration, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tenant_schedules
SET last_run_at = $3, last_status = $4, last_error = $5, runs = runs + 1,
	next_run_at = $3::timestamptz + make_interval(secs => cadence_seconds), updated_at = now()
WHERE tenant_id = $1 AND name = $2`, tenantID, name, at.UTC(), status, errMessage)
	return err
}

// List returns the tenant's persisted schedule state.
func (s *TenantScheduleStore) List(ctx context.Context, tenantID string) ([]ports.ScheduleStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT name, cadence_seconds, enabled, last_run_at, next_run_at, last_status, last_error, runs
FROM tenant_schedules WHERE tenant_id = $1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ports.ScheduleStatus
	for rows.Next() {
		var status ports.ScheduleStatus
		var cadenceSeconds int
		var enabled bool
		var lastRun, nextRun sql.NullTime
		if err := rows.Scan(&status.Name, &cadenceSeconds, &enabled, &lastRun, &nextRun, &status.LastStatus, &status.LastError, &status.Runs); err != nil {
			return nil, err
		}
		status.Cadence = time.Duration(cadenceSeconds) * time.Second
		if lastRun.Valid {
			status.LastRun = lastRun.Time
		}
		if nextRun.Valid {
			status.NextRun = nextRun.Time
		}
		result = append(result, status)
	}
	return result, rows.Err()
}

var _ interface {
	Load(context.Context, string, string) (ports.Schedule, bool, error)
	Upsert(context.Context, string, string, time.Duration, bool) error
} = (*TenantScheduleStore)(nil)
