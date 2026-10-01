package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeRuns struct {
	started  []ports.AnalysisRunRecord
	finished []ports.AnalysisRunUpdate
}

func (f *fakeRuns) StartAnalysisRun(_ context.Context, _ string, record ports.AnalysisRunRecord) (bool, error) {
	f.started = append(f.started, record)
	return true, nil
}
func (f *fakeRuns) FinishAnalysisRun(_ context.Context, _ string, _ string, update ports.AnalysisRunUpdate) (bool, error) {
	f.finished = append(f.finished, update)
	return true, nil
}
func (f *fakeRuns) ListAnalysisRuns(context.Context, string, ports.AnalysisRunFilter) ([]ports.AnalysisRunView, error) {
	return nil, nil
}
func (f *fakeRuns) LinkRunOutputs(context.Context, string, string, []string) (bool, error) {
	return true, nil
}

type fakeCandidates struct{ nodes []ports.MemoryNodeRecord }

func (f fakeCandidates) ListRecentCandidates(context.Context, string, int) ([]ports.MemoryNodeRecord, error) {
	return f.nodes, nil
}

type fakePurge struct {
	removed int
	calls   int
}

func (f *fakePurge) PurgeDeadLetterJobs(context.Context, string, time.Time) (int, error) {
	f.calls++
	return f.removed, nil
}

type fakeFeedback struct{ items []ports.MemoryFeedback }

func (f fakeFeedback) ListFeedback(context.Context, string, time.Time, int) ([]ports.MemoryFeedback, error) {
	return f.items, nil
}

func TestDedupeJobRecordsDuplicatesWithoutMutating(t *testing.T) {
	runs := &fakeRuns{}
	job := &DedupeJob{PlanName: "dedupe", Reader: fakeCandidates{nodes: []ports.MemoryNodeRecord{
		{ID: "a", ScopeType: "user-global", ScopeID: "u1", ContentText: "same"},
		{ID: "b", ScopeType: "user-global", ScopeID: "u1", ContentText: "same"},
		{ID: "c", ScopeType: "user-global", ScopeID: "u1", ContentText: "other"},
	}}, Runs: runs, NewID: func() (string, error) { return "id", nil }, Clock: func() time.Time { return time.Unix(0, 0) }}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.finished) != 1 || runs.finished[0].DiscardedCount != 1 || runs.finished[0].CandidateCount != 2 {
		t.Fatalf("dedupe run = %#v", runs.finished)
	}
}

func TestDedupeJobSkipsEmptyTenant(t *testing.T) {
	runs := &fakeRuns{}
	job := &DedupeJob{PlanName: "dedupe", Reader: fakeCandidates{}, Runs: runs}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.started) != 0 {
		t.Fatalf("empty tenant recorded %d runs, want 0", len(runs.started))
	}
}

type conflictAnalyst struct{ result ports.AnalystResult }

func (c conflictAnalyst) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	return c.result, nil
}

func TestConflictJobRecordsConflictsOnly(t *testing.T) {
	runs := &fakeRuns{}
	service := analysis.New(analysis.Providers{Analyst: conflictAnalyst{result: ports.AnalystResult{Conflicts: []ports.ConflictAssessment{{CandidateIDs: []string{"a", "b"}, Conflicted: true, ReasonCode: "contradicts"}}}}})
	job := &ConflictJob{
		PlanName: "conflict", Reader: fakeCandidates{nodes: []ports.MemoryNodeRecord{{ID: "a"}, {ID: "b"}}},
		Analysis: service, Runs: runs, PromptVersion: "llm-v1", SchemaVersion: "schema-v1",
		NewID: func() (string, error) { return "id", nil }, Clock: func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.finished) != 1 || runs.finished[0].ConflictCount != 1 {
		t.Fatalf("conflict run = %#v", runs.finished)
	}
}

func TestCleanupJobRecordsOnlyWhenItPurges(t *testing.T) {
	purge := &fakePurge{removed: 0}
	runs := &fakeRuns{}
	job := &CleanupJob{PlanName: "cleanup", Outbox: purge, Runs: runs}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.started) != 0 {
		t.Fatal("cleanup recorded a run with nothing purged")
	}
	purge.removed = 3
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.started) != 1 {
		t.Fatalf("cleanup did not record the purge: %#v", runs.started)
	}
}

func TestEvaluationJobStoresFeedbackMetrics(t *testing.T) {
	runs := &fakeRuns{}
	job := &EvaluationJob{
		PlanName: "evaluation", Runs: runs,
		Reader: fakeFeedback{items: []ports.MemoryFeedback{
			{MemoryID: "m1", Type: "helpful"}, {MemoryID: "m2", Type: "harmful"},
		}},
		NewID: func() (string, error) { return "id", nil }, Clock: func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.finished) != 1 || len(runs.finished[0].Result) == 0 {
		t.Fatalf("evaluation run = %#v", runs.finished)
	}
	var metrics map[string]any
	if err := json.Unmarshal(runs.finished[0].Result, &metrics); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if metrics["Total"].(float64) != 2 {
		t.Fatalf("metrics = %#v", metrics)
	}

	empty := &fakeRuns{}
	emptyJob := &EvaluationJob{PlanName: "evaluation", Runs: empty, Reader: fakeFeedback{}}
	if err := emptyJob.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run(empty) error = %v", err)
	}
	if len(empty.started) != 0 {
		t.Fatal("empty tenant recorded an evaluation run")
	}
}
