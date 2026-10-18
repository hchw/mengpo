package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type jobNeighbors struct {
	edges    map[string][]string
	notReady map[string]bool
}

func (f jobNeighbors) ListSemanticNeighbors(_ context.Context, _ string, query ports.SemanticNeighborQuery) ([]ports.SemanticNeighbor, error) {
	var out []ports.SemanticNeighbor
	for _, id := range f.edges[query.ReferenceMemoryID] {
		out = append(out, ports.SemanticNeighbor{Node: ports.MemoryNodeRecord{ID: id}, Similarity: 0.95})
	}
	return out, nil
}

func (f jobNeighbors) EmbeddingReady(_ context.Context, _, memoryID string) (bool, error) {
	return !f.notReady[memoryID], nil
}

type fakeMergeRepo struct {
	requests []ports.MergeRequest
	err      error
}

func (f *fakeMergeRepo) MergeMemories(_ context.Context, _ string, request ports.MergeRequest) (ports.MergeResult, error) {
	if f.err != nil {
		return ports.MergeResult{}, f.err
	}
	f.requests = append(f.requests, request)
	duplicates := make([]ports.MemoryNodeRecord, 0, len(request.DuplicateIDs))
	for _, id := range request.DuplicateIDs {
		duplicates = append(duplicates, ports.MemoryNodeRecord{ID: id})
	}
	return ports.MergeResult{Target: ports.MemoryNodeRecord{ID: request.TargetID}, Duplicates: duplicates}, nil
}

type fakeConfirmer struct {
	verdicts map[string]bool
	err      error
	calls    int
}

func (f *fakeConfirmer) ConfirmSameFact(_ context.Context, batch ports.AnalysisBatch) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	ids := []string{batch.Candidates[0].CandidateID, batch.Candidates[1].CandidateID}
	sort.Strings(ids)
	return f.verdicts[ids[0]+"|"+ids[1]], nil
}

type fakeTriageReader struct {
	records map[string]ports.CandidateTriageRecord
}

func (f fakeTriageReader) LatestCandidateTriage(_ context.Context, _ string, memoryIDs []string) (map[string]ports.CandidateTriageRecord, error) {
	out := map[string]ports.CandidateTriageRecord{}
	for _, id := range memoryIDs {
		if record, ok := f.records[id]; ok {
			out[id] = record
		}
	}
	return out, nil
}

func (f fakeTriageReader) DuplicateGroupMembers(_ context.Context, _, _ string) ([]ports.DuplicateGroupMember, error) {
	return nil, nil
}

func mergeSeeds() []ports.MemoryNodeRecord {
	return []ports.MemoryNodeRecord{
		{ID: "a", UserID: "u1", ScopeType: "user-global", ScopeID: "u1", Status: "candidate", Confidence: 0.5, ContentText: "prefers dark mode", Content: json.RawMessage(`{"text":"prefers dark mode"}`), CreatedAt: time.Unix(1, 0).UTC()},
		{ID: "b", UserID: "u1", ScopeType: "user-global", ScopeID: "u1", Status: "candidate", Confidence: 0.6, ContentText: "likes a dark theme", Content: json.RawMessage(`{"text":"likes a dark theme"}`), CreatedAt: time.Unix(2, 0).UTC()},
	}
}

func newMergeJob(reader ports.RecentCandidateReader, neighbors ports.SemanticNeighborReader) *NearDuplicateJob {
	return &NearDuplicateJob{
		PlanName: "near-duplicate", Reader: reader, Neighbors: neighbors,
		Evidence: fakeEvidence{}, Triage: fakeTriageReader{}, Runs: &fakeRuns{},
		PromptVersion: "merge-v1", SchemaVersion: "schema-v1", Threshold: 0.9,
		NewID: func() (string, error) { return "id", nil },
		Clock: func() time.Time { return time.Unix(0, 0) },
	}
}

func TestNearDuplicateJobMergesConfirmedPair(t *testing.T) {
	merge := &fakeMergeRepo{}
	confirmer := &fakeConfirmer{verdicts: map[string]bool{"a|b": true}}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.MergeEnabled = true
	job.Merge = merge
	job.Confirmer = confirmer

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(merge.requests) != 1 {
		t.Fatalf("merge requests = %#v, want 1", merge.requests)
	}
	// b has higher confidence, so it is the deterministic target.
	if merge.requests[0].TargetID != "b" || len(merge.requests[0].DuplicateIDs) != 1 || merge.requests[0].DuplicateIDs[0] != "a" {
		t.Fatalf("merge request = %#v, want b <- a", merge.requests[0])
	}
}

func TestNearDuplicateJobReportsOnlyWhenMergeDisabled(t *testing.T) {
	runs := &fakeRuns{}
	confirmer := &fakeConfirmer{verdicts: map[string]bool{"a|b": true}}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.Runs = runs
	job.MergeEnabled = false
	job.Confirmer = confirmer

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if confirmer.calls != 0 {
		t.Fatalf("report-only run called the confirmer %d times", confirmer.calls)
	}
	if len(runs.finished) != 1 {
		t.Fatalf("report runs = %#v", runs.finished)
	}
	var report nearDuplicateReport
	if err := json.Unmarshal(runs.finished[0].Result, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.DuplicatePairs != 1 || report.Merged != 0 || report.MergeEnabled {
		t.Fatalf("report = %#v", report)
	}
}

func TestNearDuplicateJobSkipsUnconfirmed(t *testing.T) {
	merge := &fakeMergeRepo{}
	confirmer := &fakeConfirmer{verdicts: map[string]bool{"a|b": false}}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.MergeEnabled = true
	job.Merge = merge
	job.Confirmer = confirmer

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(merge.requests) != 0 {
		t.Fatalf("unconfirmed pair merged: %#v", merge.requests)
	}
}

func TestNearDuplicateJobSkipsConflicting(t *testing.T) {
	confirmer := &fakeConfirmer{verdicts: map[string]bool{"a|b": true}}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.MergeEnabled = true
	job.Merge = &fakeMergeRepo{}
	job.Confirmer = confirmer
	job.Triage = fakeTriageReader{records: map[string]ports.CandidateTriageRecord{
		"a": {MemoryID: "a", ConflictIDs: []string{"b"}},
	}}

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if confirmer.calls != 0 {
		t.Fatalf("conflicting pair reached confirmation %d times", confirmer.calls)
	}
}

func TestNearDuplicateJobSkipsWhenConfirmationFails(t *testing.T) {
	merge := &fakeMergeRepo{}
	confirmer := &fakeConfirmer{err: errors.New("model unavailable")}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.MergeEnabled = true
	job.Merge = merge
	job.Confirmer = confirmer

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(merge.requests) != 0 {
		t.Fatalf("confirmation failure still merged: %#v", merge.requests)
	}
}

func TestNearDuplicateJobRecordsMissingEmbedding(t *testing.T) {
	runs := &fakeRuns{}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{notReady: map[string]bool{"a": true, "b": true}})
	job.Runs = runs
	job.MergeEnabled = true
	job.Merge = &fakeMergeRepo{}
	job.Confirmer = &fakeConfirmer{}

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var report nearDuplicateReport
	if err := json.Unmarshal(runs.finished[0].Result, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.SkippedNoEmbedding != 2 {
		t.Fatalf("skipped_no_embedding = %d, want 2", report.SkippedNoEmbedding)
	}
}

func TestNearDuplicateJobSkipsVersionConflict(t *testing.T) {
	merge := &fakeMergeRepo{err: ports.ErrInvalidMerge}
	runs := &fakeRuns{}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.Runs = runs
	job.MergeEnabled = true
	job.Merge = merge
	job.Confirmer = &fakeConfirmer{verdicts: map[string]bool{"a|b": true}}

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("version conflict should be skipped, got %v", err)
	}
	var report nearDuplicateReport
	if err := json.Unmarshal(runs.finished[0].Result, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.SkippedVersion != 1 || report.Merged != 0 {
		t.Fatalf("report = %#v, want skipped_version=1 merged=0", report)
	}
}

func TestNearDuplicateJobReportsCounts(t *testing.T) {
	runs := &fakeRuns{}
	job := newMergeJob(fakeCandidates{nodes: mergeSeeds()}, jobNeighbors{edges: map[string][]string{"a": {"b"}}})
	job.Runs = runs
	job.MergeEnabled = true
	job.Merge = &fakeMergeRepo{}
	job.Confirmer = &fakeConfirmer{verdicts: map[string]bool{"a|b": true}}

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var report nearDuplicateReport
	if err := json.Unmarshal(runs.finished[0].Result, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Scanned != 2 || report.DuplicatePairs != 1 || report.Merged != 1 || !report.MergeEnabled {
		t.Fatalf("report = %#v, want scanned=2 pairs=1 merged=1", report)
	}
}

func TestChooseMergeTargetPrecedence(t *testing.T) {
	members := []ports.MemoryNodeRecord{
		{ID: "candidate", Status: "candidate", Confidence: 0.99},
		{ID: "active", Status: "active", Confidence: 0.5},
		{ID: "stable", Status: "stable", Confidence: 0.1},
	}
	if target := chooseMergeTarget(members, nil); target.ID != "stable" {
		t.Fatalf("target = %q, want stable", target.ID)
	}
	// Equal status: higher confidence wins.
	actives := []ports.MemoryNodeRecord{
		{ID: "low", Status: "active", Confidence: 0.2},
		{ID: "high", Status: "active", Confidence: 0.8},
	}
	if target := chooseMergeTarget(actives, nil); target.ID != "high" {
		t.Fatalf("target = %q, want high", target.ID)
	}
	// Equal status and confidence: more evidence wins.
	tie := []ports.MemoryNodeRecord{{ID: "few", Status: "active", Confidence: 0.5}, {ID: "many", Status: "active", Confidence: 0.5}}
	if target := chooseMergeTarget(tie, map[string]int{"few": 1, "many": 3}); target.ID != "many" {
		t.Fatalf("target = %q, want many", target.ID)
	}
}
