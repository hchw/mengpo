package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func TestBuildPipelineResultMaintainsEvidenceLineage(t *testing.T) {
	now := time.Now()
	raw := []RawEvent{{ID: "raw-1", TenantID: "tenant-a", SessionID: "session-1", OccurredAt: now, SourceType: "user", MessageType: "message", Payload: json.RawMessage(`{"text":"hello"}`)}, {ID: "raw-2", TenantID: "tenant-a", SessionID: "session-1", OccurredAt: now, SourceType: "tool", MessageType: "tool.result", Payload: json.RawMessage(`{"status":"failed"}`)}}
	analysis := ports.AnalystResult{Candidates: []ports.CandidateMemory{{CandidateID: "candidate-1", EvidenceEventIDs: []string{"raw-1"}, ScopeType: "session", ScopeID: "session-1", Content: json.RawMessage(`{"fact":"hello"}`), Confidence: .7}}, Failures: []ports.FailureAssessment{{EventIDs: []string{"raw-2"}, Conclusion: "suspected_failure", Attribution: "inferred", Confidence: .6}}}
	got, err := BuildPipelineResult("tenant-a", "schema-v1", "normalizer-v1", raw, analysis, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Normalized) != 2 || got.Normalized[0].RawEventID != "raw-1" || got.Normalized[1].RawEventID != "raw-2" {
		t.Fatalf("normalized lineage=%#v", got.Normalized)
	}
	if len(got.Summaries) != 1 || got.Summaries[0].Text != "hello" || len(got.Summaries[0].EventIDs) != 2 {
		t.Fatalf("summary=%#v", got.Summaries)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].CandidateID != "candidate-1" || len(got.Failures) != 1 || got.Failures[0].SessionID != "session-1" {
		t.Fatalf("analysis output=%#v", got)
	}
}

func TestBuildPipelineResultRejectsForeignTenantOrEvidence(t *testing.T) {
	now := time.Now()
	raw := []RawEvent{{ID: "raw", TenantID: "tenant-a", OccurredAt: now, Payload: json.RawMessage(`{}`)}}
	if _, err := BuildPipelineResult("tenant-a", "schema", "normalizer", raw, ports.AnalystResult{Failures: []ports.FailureAssessment{{EventIDs: []string{"foreign"}, Conclusion: "failure", Confidence: .4}}}, now); !errors.Is(err, ErrInvalidAnalysisResult) {
		t.Fatalf("foreign evidence error=%v", err)
	}
	raw[0].TenantID = "tenant-b"
	if _, err := BuildPipelineResult("tenant-a", "schema", "normalizer", raw, ports.AnalystResult{}, now); !errors.Is(err, ErrInvalidPipelineInput) {
		t.Fatalf("cross-tenant raw event error=%v", err)
	}
}

func TestEndToEndAnalysisNormalizationCreatesOnlyCandidates(t *testing.T) {
	now := time.Now().UTC()
	raw := []RawEvent{
		{ID: "raw-user", TenantID: "tenant-x", SessionID: "session-x", OccurredAt: now, SourceType: "user", MessageType: "message", Payload: json.RawMessage(`{"text":"I prefer concise answers"}`)},
		{ID: "raw-tool", TenantID: "tenant-x", SessionID: "session-x", OccurredAt: now, SourceType: "tool", MessageType: "tool.result", Payload: json.RawMessage(`{"status":"failed"}`)},
	}
	batch := ports.AnalysisBatch{TenantID: "tenant-x", RunID: "run-x", PromptVersion: "p1", SchemaVersion: "s1", Events: []ports.AnalysisEvent{{EventID: "raw-user", SessionID: "session-x", OccurredAt: now, Payload: raw[0].Payload}, {EventID: "raw-tool", SessionID: "session-x", OccurredAt: now, Payload: raw[1].Payload}}}
	analyst := &mockAnalyst{result: ports.AnalystResult{
		Candidates: []ports.CandidateMemory{{CandidateID: "candidate-x", EvidenceEventIDs: []string{"raw-user"}, ScopeType: "session", ScopeID: "session-x", Content: json.RawMessage(`{"preference":"concise answers"}`), Confidence: .72}},
		Failures:   []ports.FailureAssessment{{EventIDs: []string{"raw-tool"}, Conclusion: "suspected_failure", Attribution: "inferred", Confidence: .55}},
	}}
	service := New(Providers{Analyst: analyst})
	result, err := service.Analyze(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := BuildPipelineResult("tenant-x", "schema-v1", "normalizer-v1", raw, result, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pipeline.Normalized) != 2 || len(pipeline.Summaries) != 1 || len(pipeline.Candidates) != 1 || len(pipeline.Failures) != 1 {
		t.Fatalf("pipeline=%#v", pipeline)
	}
	if pipeline.Candidates[0].CandidateID != "candidate-x" || pipeline.Failures[0].Conclusion != "suspected_failure" {
		t.Fatalf("candidates/failures=%#v %#v", pipeline.Candidates, pipeline.Failures)
	}
	// Results remain candidate records; the pipeline has no path that promotes or persists Stable Memory.
	if analyst.got.TenantID != "tenant-x" {
		t.Fatalf("analyst received tenant %q", analyst.got.TenantID)
	}
}
