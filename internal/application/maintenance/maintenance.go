// Package maintenance implements the periodic memory-maintenance plan. It
// selects a batch of normalized events for a tenant and enqueues a durable
// outbox job; the durable worker performs the model call. Keeping the model
// call on the durable path means retries, dead-lettering and idempotency are
// shared with event-driven analysis.
package maintenance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/hchw/mengpo/internal/ports"
)

// JobTypeConsolidate is the durable outbox job that carries periodic
// consolidation work.
const JobTypeConsolidate = "consolidate"

// TaskTypeConsolidate is the analysis task the model runs for this plan.
const TaskTypeConsolidate = "consolidate_memory"

// Job is a periodic maintenance plan for one task type. It is safe to run from
// many replicas: the scheduler holds a per-(tenant, plan) lock and the enqueued
// job's idempotency key is stable for a given cursor position.
type Job struct {
	PlanName  string
	TaskType  string
	BatchSize int
	Reader    ports.NormalizedEventReader
	Enqueuer  ports.OutboxEnqueuer
	Cursors   ports.MaintenanceCursorStore
	NewID     func() (string, error)
}

// Name implements ports.ScheduledJob.
func (j *Job) Name() string { return j.PlanName }

// Run selects one batch of pending normalized events for the tenant and
// enqueues the consolidation job. A tenant with no pending events is skipped
// without any side effect.
func (j *Job) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Enqueuer == nil || j.Cursors == nil {
		return errors.New("maintenance job is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	offset, err := j.Cursors.LoadMaintenanceCursor(ctx, tenantID, j.TaskType)
	if err != nil {
		return fmt.Errorf("load maintenance cursor: %w", err)
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}
	events, err := j.Reader.ListPendingAnalysisEvents(ctx, tenantID, int(offset), batchSize)
	if err != nil {
		return fmt.Errorf("list pending analysis events: %w", err)
	}
	if len(events) == 0 {
		// Nothing to do: no outbox job, no cursor movement, no candidates.
		return nil
	}
	rawIDs := make([]string, 0, len(events))
	for _, event := range events {
		rawIDs = append(rawIDs, event.RawEventID)
	}
	payload, err := json.Marshal(map[string]any{
		"raw_event_ids": rawIDs,
		"task_type":     j.TaskType,
		"window_offset": offset,
	})
	if err != nil {
		return fmt.Errorf("encode consolidate job payload: %w", err)
	}
	newID := j.NewID
	if newID == nil {
		newID = uuid
	}
	id, err := newID()
	if err != nil {
		return err
	}
	if err := j.Enqueuer.EnqueueJob(ctx, ports.OutboxJob{
		ID:             id,
		TenantID:       tenantID,
		JobType:        JobTypeConsolidate,
		IdempotencyKey: idempotencyKey(tenantID, j.TaskType, offset),
		Payload:        payload,
	}); err != nil {
		return fmt.Errorf("enqueue consolidate job: %w", err)
	}
	if err := j.Cursors.SaveMaintenanceCursor(ctx, tenantID, j.TaskType, offset+int64(len(events))); err != nil {
		return fmt.Errorf("save maintenance cursor: %w", err)
	}
	return nil
}

// idempotencyKey is stable for a cursor position so replaying a maintenance
// window enqueues at most one job.
func idempotencyKey(tenantID, taskType string, offset int64) string {
	return "maintenance:" + tenantID + ":" + taskType + ":" + strconv.FormatInt(offset, 10)
}

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(b[:])
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}
