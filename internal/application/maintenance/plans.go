package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/evaluation"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// These plans are deliberately conservative: they observe and record, and they
// never promote or mutate memory directly. Promotion stays with governance.

// DedupeJob detects duplicate candidates and records the finding. It never
// merges or promotes: automatic merging would bypass the governance gate.
type DedupeJob struct {
	PlanName  string
	Reader    ports.RecentCandidateReader
	Runs      ports.AnalysisRunStore
	BatchSize int
	NewID     func() (string, error)
	Clock     func() time.Time
}

func (j *DedupeJob) Name() string { return j.PlanName }

func (j *DedupeJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Runs == nil {
		return errors.New("dedupe job is not configured")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	candidates, err := j.Reader.ListRecentCandidates(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	groups := map[string]struct{}{}
	for _, node := range candidates {
		groups[duplicateKey(node)] = struct{}{}
	}
	duplicates := len(candidates) - len(groups)
	now := j.now()
	return recordRun(ctx, j.Runs, j.newID, runSpec{
		tenantID:       tenantID,
		plan:           j.PlanName,
		task:           "dedupe_scan",
		now:            now,
		candidateCount: len(groups),
		discardedCount: duplicates,
	})
}

func duplicateKey(node ports.MemoryNodeRecord) string {
	sum := sha256.Sum256([]byte(node.ScopeType + "|" + node.ScopeID + "|" + node.ContentText))
	return hex.EncodeToString(sum[:])
}

// ConflictJob asks the Memory LLM to find conflicts among recent candidates and
// records them. It records the conflict count; it never changes candidate state.
type ConflictJob struct {
	PlanName      string
	Reader        ports.RecentCandidateReader
	Analysis      *analysis.Service
	Runs          ports.AnalysisRunStore
	PromptVersion string
	SchemaVersion string
	BatchSize     int
	NewID         func() (string, error)
	Clock         func() time.Time
}

func (j *ConflictJob) Name() string { return j.PlanName }

func (j *ConflictJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Analysis == nil || j.Runs == nil {
		return errors.New("conflict job is not configured")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	nodes, err := j.Reader.ListRecentCandidates(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	if len(nodes) < 2 {
		return nil
	}
	candidates := make([]ports.CandidateMemory, 0, len(nodes))
	for _, node := range nodes {
		candidates = append(candidates, ports.CandidateMemory{
			CandidateID: node.ID, ScopeType: node.ScopeType, ScopeID: node.ScopeID,
			Content: node.Content, Confidence: node.Confidence,
		})
	}
	now := j.now()
	correlation := correlationID(j.PlanName, tenantID, now)
	if _, err := j.Runs.StartAnalysisRun(ctx, tenantID, ports.AnalysisRunRecord{
		ID: j.id(), RunID: correlation, TaskType: observation.TaskConflictScan, Trigger: ports.TriggerSchedule,
		PromptVersion: j.PromptVersion, SchemaVersion: j.SchemaVersion, Status: ports.AnalysisRunRunning,
	}); err != nil {
		return err
	}
	started := j.now()
	result, analyzeErr := j.Analysis.Analyze(ctx, ports.AnalysisBatch{
		TenantID: tenantID, RunID: correlation, TaskType: observation.TaskConflictScan,
		PromptVersion: j.PromptVersion, SchemaVersion: j.SchemaVersion, Candidates: candidates,
	})
	if analyzeErr != nil {
		_, _ = j.Runs.FinishAnalysisRun(ctx, tenantID, correlation, ports.AnalysisRunUpdate{
			Status: ports.AnalysisRunFailed, LastError: analyzeErr.Error(),
		})
		return analyzeErr
	}
	conflicts := 0
	for _, item := range result.Conflicts {
		if item.Conflicted {
			conflicts++
		}
	}
	_, err = j.Runs.FinishAnalysisRun(ctx, tenantID, correlation, ports.AnalysisRunUpdate{
		Status: ports.AnalysisRunSucceeded, ConflictCount: conflicts,
		LatencyMS: j.now().Sub(started).Milliseconds(),
	})
	return err
}

// CleanupJob purges dead-letter outbox jobs past their retention window.
type CleanupJob struct {
	PlanName  string
	Outbox    ports.OutboxMaintenanceRepository
	Runs      ports.AnalysisRunStore
	Retention time.Duration
	NewID     func() (string, error)
	Clock     func() time.Time
}

func (j *CleanupJob) Name() string { return j.PlanName }

func (j *CleanupJob) Run(ctx context.Context, tenantID string) error {
	if j.Outbox == nil {
		return errors.New("cleanup job is not configured")
	}
	retention := j.Retention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	removed, err := j.Outbox.PurgeDeadLetterJobs(ctx, tenantID, j.now().Add(-retention))
	if err != nil {
		return err
	}
	if removed == 0 {
		return nil
	}
	return recordRun(ctx, j.Runs, j.newID, runSpec{
		tenantID: tenantID, plan: j.PlanName, task: "retry_cleanup", now: j.now(),
	})
}

// EvaluationJob refreshes the tenant's feedback metrics on a schedule.
type EvaluationJob struct {
	PlanName    string
	Reader      ports.FeedbackReader
	Runs        ports.AnalysisRunStore
	SinceWindow time.Duration
	Limit       int
	NewID       func() (string, error)
	Clock       func() time.Time
}

func (j *EvaluationJob) Name() string { return j.PlanName }

func (j *EvaluationJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Runs == nil {
		return errors.New("evaluation job is not configured")
	}
	window := j.SinceWindow
	if window <= 0 {
		window = 30 * 24 * time.Hour
	}
	limit := j.Limit
	if limit <= 0 {
		limit = 5000
	}
	now := j.now()
	feedback, err := j.Reader.ListFeedback(ctx, tenantID, now.Add(-window), limit)
	if err != nil {
		return err
	}
	if len(feedback) == 0 {
		return nil
	}
	events := make([]evaluation.FeedbackEvent, 0, len(feedback))
	for _, item := range feedback {
		events = append(events, evaluation.FeedbackEvent{MemoryID: item.MemoryID, Type: evaluation.FeedbackType(item.Type)})
	}
	metrics, err := evaluation.ComputeFeedbackMetrics(events)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	return recordRun(ctx, j.Runs, j.newID, runSpec{
		tenantID: tenantID, plan: j.PlanName, task: "evaluate_feedback", now: now, result: encoded,
	})
}

type runSpec struct {
	tenantID       string
	plan           string
	task           string
	now            time.Time
	candidateCount int
	discardedCount int
	result         json.RawMessage
}

func recordRun(ctx context.Context, runs ports.AnalysisRunStore, newID func() (string, error), spec runSpec) error {
	if runs == nil {
		return nil
	}
	correlation := correlationID(spec.plan, spec.tenantID, spec.now)
	id := "analysis"
	if newID != nil {
		generated, err := newID()
		if err != nil {
			return err
		}
		id = generated
	}
	if _, err := runs.StartAnalysisRun(ctx, spec.tenantID, ports.AnalysisRunRecord{
		ID: id, RunID: correlation, TaskType: spec.task, Trigger: ports.TriggerSchedule, Status: ports.AnalysisRunRunning,
	}); err != nil {
		return err
	}
	_, err := runs.FinishAnalysisRun(ctx, spec.tenantID, correlation, ports.AnalysisRunUpdate{
		Status: ports.AnalysisRunSucceeded, CandidateCount: spec.candidateCount, DiscardedCount: spec.discardedCount, Result: spec.result,
	})
	return err
}

// correlationID is stable within the plan's window so a re-run of the same
// window does not create duplicate records.
func correlationID(plan, tenantID string, now time.Time) string {
	return fmt.Sprintf("plan:%s:%s:%d", plan, tenantID, now.UTC().Truncate(24*time.Hour).Unix())
}

func (j *DedupeJob) now() time.Time         { return clock(j.Clock) }
func (j *DedupeJob) newID() (string, error) { return newIDOr(j.NewID) }
func (j *ConflictJob) now() time.Time       { return clock(j.Clock) }
func (j *ConflictJob) id() string {
	if value, err := newIDOr(j.NewID); err == nil {
		return value
	}
	return "analysis"
}
func (j *CleanupJob) now() time.Time            { return clock(j.Clock) }
func (j *CleanupJob) newID() (string, error)    { return newIDOr(j.NewID) }
func (j *EvaluationJob) newID() (string, error) { return newIDOr(j.NewID) }
func (j *EvaluationJob) now() time.Time         { return clock(j.Clock) }

func clock(fn func() time.Time) time.Time {
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

func newIDOr(fn func() (string, error)) (string, error) {
	if fn != nil {
		return fn()
	}
	return uuid()
}
