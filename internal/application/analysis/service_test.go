package analysis

import (
	"context"
	"errors"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

type mockClassifier struct{ got ports.AnalysisBatch }

func (m *mockClassifier) Classify(_ context.Context, b ports.AnalysisBatch) ([]ports.Classification, error) {
	m.got = b
	return []ports.Classification{{EventID: b.Events[0].EventID, Category: "tool_result", Confidence: .9}}, nil
}

type mockFailureAnalyzer struct{ got ports.AnalysisBatch }

func (m *mockFailureAnalyzer) AnalyzeFailures(_ context.Context, b ports.AnalysisBatch) ([]ports.FailureAssessment, error) {
	m.got = b
	return []ports.FailureAssessment{{EventIDs: []string{b.Events[0].EventID}, Conclusion: "suspected_failure", Attribution: "inferred", Confidence: .6}}, nil
}

type mockConsolidator struct{ got ports.AnalysisBatch }

func (m *mockConsolidator) Consolidate(_ context.Context, b ports.AnalysisBatch) ([]ports.CandidateMemory, error) {
	m.got = b
	return []ports.CandidateMemory{{CandidateID: "candidate-1", EvidenceEventIDs: []string{b.Events[0].EventID}, ScopeType: "session", ScopeID: b.Events[0].SessionID, Content: []byte(`{"summary":"candidate"}`), Confidence: .7}}, nil
}

type mockConflictAnalyzer struct {
	tenant string
	count  int
}

func (m *mockConflictAnalyzer) AnalyzeConflicts(_ context.Context, b ports.AnalysisBatch, c []ports.CandidateMemory) ([]ports.ConflictAssessment, error) {
	m.tenant = b.TenantID
	m.count = len(c)
	return []ports.ConflictAssessment{{CandidateIDs: []string{c[0].CandidateID}, Conflicted: false}}, nil
}

type mockAnalyst struct {
	got    ports.AnalysisBatch
	err    error
	result ports.AnalystResult
}

func (m *mockAnalyst) Analyze(_ context.Context, b ports.AnalysisBatch) (ports.AnalystResult, error) {
	m.got = b
	return m.result, m.err
}

func validBatch() ports.AnalysisBatch {
	return ports.AnalysisBatch{TenantID: "tenant-a", RunID: "run-1", PromptVersion: "prompt-v1", SchemaVersion: "schema-v1", Events: []ports.AnalysisEvent{{EventID: "event-1", SessionID: "session"}}}
}

func TestComposableProvidersReceiveTenantAndVersionedBatch(t *testing.T) {
	classifier, failure, consolidator, conflicts := &mockClassifier{}, &mockFailureAnalyzer{}, &mockConsolidator{}, &mockConflictAnalyzer{}
	service := New(Providers{Classifier: classifier, FailureAnalyzer: failure, Consolidator: consolidator, ConflictAnalyzer: conflicts})
	got, err := service.Analyze(context.Background(), validBatch())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Classifications) != 1 || len(got.Failures) != 1 || len(got.Candidates) != 1 || len(got.Conflicts) != 1 {
		t.Fatalf("result=%#v", got)
	}
	for name, batch := range map[string]ports.AnalysisBatch{"classifier": classifier.got, "failure": failure.got, "consolidator": consolidator.got} {
		if batch.TenantID != "tenant-a" || batch.PromptVersion != "prompt-v1" || batch.SchemaVersion != "schema-v1" {
			t.Errorf("%s received context %#v", name, batch)
		}
	}
	if conflicts.tenant != "tenant-a" || conflicts.count != 1 {
		t.Fatalf("conflict provider context tenant=%s candidates=%d", conflicts.tenant, conflicts.count)
	}
}

func TestUnifiedAnalystTakesPrecedence(t *testing.T) {
	analyst := &mockAnalyst{}
	classifier := &mockClassifier{}
	service := New(Providers{Analyst: analyst, Classifier: classifier})
	if _, err := service.Analyze(context.Background(), validBatch()); err != nil {
		t.Fatal(err)
	}
	if analyst.got.TenantID != "tenant-a" || len(analyst.got.Events) != len(validBatch().Events) || analyst.got.Events[0].EventID != validBatch().Events[0].EventID {
		t.Fatalf("analyst context=%#v", analyst.got)
	}
	if classifier.got.TenantID != "" {
		t.Fatal("classifier called when unified analyst configured")
	}
}

func TestAnalysisRejectsMissingTenantAndWrapsProviderErrors(t *testing.T) {
	service := New(Providers{Analyst: &mockAnalyst{err: errors.New("timeout")}})
	if _, err := service.Analyze(context.Background(), ports.AnalysisBatch{}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("validation error=%v", err)
	}
	if _, err := service.Analyze(context.Background(), validBatch()); err == nil {
		t.Fatal("expected provider error")
	}
}
