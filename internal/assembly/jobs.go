package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/ports"
)

// ErrUnsupportedJobType marks an outbox job the worker does not understand. It
// is returned (not swallowed) so the durable retry/dead-letter path records it.
var ErrUnsupportedJobType = errors.New("unsupported outbox job type")

// Job type identifiers persisted in outbox_jobs.
const (
	JobNormalizeEvent = "normalize_event"
	JobConsolidate    = ConsolidateJobType
)

// JobDispatcher routes durable outbox jobs to handlers. Normalization turns a
// raw observation into a persisted normalized event using the rule-based
// analyst; consolidation is acknowledged because Memory LLM consolidation is
// out of scope and the rule baseline produces no candidates to consolidate.
type JobDispatcher struct {
	Observations         ports.ObservationReader
	Normalized           ports.NormalizedEventStore
	Analysis             *analysis.Service
	SchemaVersion        string
	NormalizationVersion string
	NewID                func() (string, error)
}

// Handle implements workers.JobHandler.
func (d *JobDispatcher) Handle(ctx context.Context, job ports.OutboxJob) error {
	switch job.JobType {
	case JobNormalizeEvent:
		return d.normalizeEvent(ctx, job)
	case JobConsolidate:
		// Rule-based degradation never creates candidates; there is nothing to
		// consolidate without an external Memory LLM. Acknowledge the job so it
		// does not dead-letter, and rely on the projection/failure paths instead.
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedJobType, job.JobType)
	}
}

func (d *JobDispatcher) normalizeEvent(ctx context.Context, job ports.OutboxJob) error {
	if d == nil || d.Observations == nil || d.Normalized == nil || d.Analysis == nil {
		return errors.New("normalization job handler is not configured")
	}
	rawEventID := job.AggregateID
	if rawEventID == "" {
		var payload struct {
			RawEventID string `json:"raw_event_id"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("decode normalize job payload: %w", err)
		}
		rawEventID = payload.RawEventID
	}
	if rawEventID == "" {
		return errors.New("normalize job has no raw event id")
	}
	event, err := d.Observations.GetObservation(ctx, job.TenantID, rawEventID)
	if err != nil {
		return err
	}
	result, err := d.Analysis.Analyze(ctx, ports.AnalysisBatch{
		TenantID:      job.TenantID,
		RunID:         job.ID,
		PromptVersion: "rule-v1",
		SchemaVersion: d.SchemaVersion,
		Events:        []ports.AnalysisEvent{{EventID: event.ID, SessionID: event.SessionID, OccurredAt: event.OccurredAt, Payload: event.Payload}},
	})
	if err != nil {
		return err
	}
	pipeline, err := analysis.BuildPipelineResult(job.TenantID, d.SchemaVersion, d.NormalizationVersion, []analysis.RawEvent{{
		ID:            event.ID,
		TenantID:      job.TenantID,
		SessionID:     event.SessionID,
		OccurredAt:    event.OccurredAt,
		SourceType:    string(event.SourceType),
		MessageType:   event.MessageType,
		Payload:       event.Payload,
		Sequence:      event.Sequence,
		ParentEventID: event.ParentEventID,
	}}, result, time.Now().UTC())
	if err != nil {
		return err
	}
	newID := d.NewID
	if newID == nil {
		newID = newUUID
	}
	records := make([]ports.NormalizedEventRecord, 0, len(pipeline.Normalized))
	for _, item := range pipeline.Normalized {
		id, err := newID()
		if err != nil {
			return err
		}
		records = append(records, ports.NormalizedEventRecord{
			ID:                   id,
			RawEventID:           item.RawEventID,
			SchemaVersion:        item.SchemaVersion,
			NormalizationVersion: item.NormalizationVersion,
			Sequence:             item.Sequence,
			ParentEventID:        item.ParentEventID,
			Payload:              item.Payload,
		})
	}
	if err := d.Normalized.StoreNormalizedEvents(ctx, job.TenantID, records); err != nil {
		return err
	}
	return d.Normalized.MarkRawEventProcessed(ctx, job.TenantID, event.ID)
}
