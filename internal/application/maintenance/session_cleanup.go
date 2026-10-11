package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

// SessionCleanupJob removes bound sessions that never produced any activity.
//
// A session row is created eagerly at bind time because it carries ownership and
// authorization, so without a purge the sessions table accumulates rows that the
// console explorer intentionally hides. The job is conservative: a session is
// removed only when it is older than the retention window and owns no event,
// memory, run or retrieval record. Every removal is appended to the audit trail.
type SessionCleanupJob struct {
	PlanName  string
	Sessions  ports.IdleSessionStore
	Audit     ports.AuditWriter
	Retention time.Duration
	BatchSize int
	Clock     func() time.Time
}

// Name implements ports.ScheduledJob.
func (j *SessionCleanupJob) Name() string { return j.PlanName }

// Run removes one batch of idle sessions for the tenant. A tenant with nothing
// idle is skipped without any side effect.
func (j *SessionCleanupJob) Run(ctx context.Context, tenantID string) error {
	if j.Sessions == nil {
		return errors.New("session cleanup job is not configured")
	}
	retention := j.Retention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	cutoff := j.now().Add(-retention)
	idle, err := j.Sessions.ListIdleSessions(ctx, tenantID, cutoff, batchSize)
	if err != nil {
		return err
	}
	for _, session := range idle {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Re-check emptiness in the delete transaction: a session that just
		// received an event between listing and deleting must be kept.
		removed, err := j.Sessions.DeleteIdleSession(ctx, tenantID, session.ID, cutoff)
		if err != nil {
			return err
		}
		if !removed {
			continue
		}
		j.recordRemoval(ctx, tenantID, session, retention)
	}
	return nil
}

// recordRemoval appends an audit event so who/when/what/why is recoverable. An
// audit failure never blocks the cleanup batch.
func (j *SessionCleanupJob) recordRemoval(ctx context.Context, tenantID string, session ports.SessionSummaryRecord, retention time.Duration) {
	if j.Audit == nil {
		return
	}
	changes, err := json.Marshal(map[string]any{
		"reason":         "idle session with no events or memories",
		"started_at":     session.StartedAt.UTC().Format(time.RFC3339),
		"retention_days": int(retention / (24 * time.Hour)),
		"removed_at":     j.now().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = j.Audit.RecordAuditEvent(ctx, tenantID, ports.AuditEventRecord{
		ActorType:    "system",
		ActorID:      j.PlanName,
		Action:       "session.cleanup",
		ResourceType: "session",
		ResourceID:   session.ID,
		Changes:      changes,
	})
}

func (j *SessionCleanupJob) now() time.Time { return clock(j.Clock) }
