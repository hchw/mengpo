package ports

import (
	"context"
	"encoding/json"
	"time"
)

// Analysis run statuses persisted in analysis_jobs.
const (
	AnalysisRunRunning    = "running"
	AnalysisRunSucceeded  = "succeeded"
	AnalysisRunFailed     = "failed"
	AnalysisRunDeadLetter = "dead_letter"
	AnalysisRunCancelled  = "cancelled"
)

// Analysis run trigger sources. The trigger is recorded so a run can be traced
// back to what caused it.
const (
	TriggerEvent    = "event"
	TriggerExplicit = "explicit"
	TriggerSchedule = "schedule"
)

// AnalysisRunRecord opens an audit record for one curated analysis execution.
type AnalysisRunRecord struct {
	ID             string
	RunID          string // unique correlation id (outbox job id)
	TenantID       string
	TaskType       string
	Trigger        string
	Provider       string
	Model          string
	PromptVersion  string
	SchemaVersion  string
	SessionID      string
	InputWatermark *int64
	InputEventIDs  []string
	Priority       int
	Status         string
	CreatedAt      time.Time
}

// AnalysisRunUpdate carries the terminal fields of a run.
type AnalysisRunUpdate struct {
	// Result holds optional structured output (for example an evaluation
	// report). OutputMemoryIDs links the run to the memories it produced so a
	// memory deletion can clean up the related run records.
	Result           json.RawMessage
	OutputMemoryIDs  []string
	Status           string
	Attempts         int
	LastError        string
	LatencyMS        int64
	TokensPrompt     int
	TokensCompletion int
	CostUSD          float64
	CandidateCount   int
	DiscardedCount   int
	ConflictCount    int
	DegradedReason   string
}

// AnalysisRunView is one run record as returned to the console.
type AnalysisRunView struct {
	AnalysisRunRecord
	LatencyMS        int64
	TokensPrompt     int
	TokensCompletion int
	CostUSD          float64
	CandidateCount   int
	DiscardedCount   int
	ConflictCount    int
	DegradedReason   string
	LastError        string
	Attempts         int
	UpdatedAt        time.Time
	Result           json.RawMessage
}

// AnalysisRunFilter filters run records for the console.
type AnalysisRunFilter struct {
	SessionID string
	TaskType  string
	Status    string
	Since     *time.Time
	Until     *time.Time
	Page      int
	PageSize  int
}

// AnalysisRunStore records every curated analysis run for audit. Starting a run
// is idempotent on the correlation id so retries never duplicate records.
type AnalysisRunStore interface {
	StartAnalysisRun(ctx context.Context, tenantID string, record AnalysisRunRecord) (bool, error)
	FinishAnalysisRun(ctx context.Context, tenantID, runID string, update AnalysisRunUpdate) (bool, error)
	ListAnalysisRuns(ctx context.Context, tenantID string, filter AnalysisRunFilter) ([]AnalysisRunView, error)
	// LinkRunOutputs records the memories a run produced so a later deletion can
	// clean up the run record.
	LinkRunOutputs(ctx context.Context, tenantID, runID string, memoryIDs []string) (bool, error)
}

// MaintenanceCursorStore tracks how far periodic maintenance has processed a
// tenant's normalized events, so a maintenance run is resumable and idempotent.
type MaintenanceCursorStore interface {
	LoadMaintenanceCursor(ctx context.Context, tenantID, taskType string) (int64, error)
	SaveMaintenanceCursor(ctx context.Context, tenantID, taskType string, watermark int64) error
}

// DueMemoryReader selects memories whose TTL has elapsed so periodic
// maintenance can expire them through governance (never by a bare update).
type DueMemoryReader interface {
	ListDueMemories(ctx context.Context, tenantID string, limit int) ([]MemoryNodeRecord, error)
}

// RecentCandidateReader lists recently updated candidates for a conflict scan.
type RecentCandidateReader interface {
	ListRecentCandidates(ctx context.Context, tenantID string, limit int) ([]MemoryNodeRecord, error)
}

// FeedbackReader lists tenant feedback so the evaluation plan can compute
// feedback metrics.
type FeedbackReader interface {
	ListFeedback(ctx context.Context, tenantID string, since time.Time, limit int) ([]MemoryFeedback, error)
}

// OutboxMaintenanceRepository purges durable jobs that exceeded their retention
// window.
type OutboxMaintenanceRepository interface {
	PurgeDeadLetterJobs(ctx context.Context, tenantID string, olderThan time.Time) (int, error)
}
