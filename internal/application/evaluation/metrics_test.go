package evaluation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestComputeFeedbackMetricsKeepsNegativeSignalsSeparate(t *testing.T) {
	events := []FeedbackEvent{
		{MemoryID: "m1", Type: FeedbackHelpful},
		{MemoryID: "m1", Type: FeedbackHelpful},
		{MemoryID: "m2", Type: FeedbackHarmful},
		{MemoryID: "m3", Type: FeedbackCorrected},
		{MemoryID: "m3", Type: FeedbackAccepted},
		{MemoryID: "m4", Type: FeedbackIgnored},
		{MemoryID: "m5", Type: FeedbackConflict},
		{MemoryID: "m6", Type: FeedbackExpired},
	}
	metrics, err := ComputeFeedbackMetrics(events)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Total != 8 || metrics.Counts[FeedbackHelpful] != 2 {
		t.Fatalf("metrics=%#v", metrics)
	}
	if metrics.HelpfulRate != 0.25 || metrics.HarmfulRate != 0.125 || metrics.ConflictRate != 0.125 || metrics.CorrectionRate != 0.125 {
		t.Fatalf("rates=%#v", metrics)
	}
	if metrics.IgnoredRate != 0.25 {
		t.Fatalf("ignored rate=%#v", metrics)
	}
	if _, err := ComputeFeedbackMetrics([]FeedbackEvent{{MemoryID: "m", Type: "unknown"}}); err == nil {
		t.Fatal("unknown feedback type accepted")
	}
}

func TestFeedbackMetricsSnapshot(t *testing.T) {
	metrics, err := ComputeFeedbackMetrics([]FeedbackEvent{
		{MemoryID: "m1", Type: FeedbackHelpful},
		{MemoryID: "m1", Type: FeedbackAccepted},
		{MemoryID: "m2", Type: FeedbackHarmful},
		{MemoryID: "m3", Type: FeedbackConflict},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(struct {
		Total  int                `json:"total"`
		Counts any                `json:"counts"`
		Rates  map[string]float64 `json:"rates"`
	}{Total: metrics.Total, Counts: metrics.SortedCounts(), Rates: map[string]float64{
		"helpful": metrics.HelpfulRate, "harmful": metrics.HarmfulRate, "ignored": metrics.IgnoredRate,
		"conflict": metrics.ConflictRate, "correction": metrics.CorrectionRate,
	}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "feedback_metrics.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1): %v", err)
	}
	if string(encoded) != string(trimNewline(expected)) {
		t.Fatalf("feedback metrics snapshot changed:\n got:\n%s\nwant:\n%s", encoded, expected)
	}
}

func TestEvaluateRetrievalCountsWrongMemoriesAndCosts(t *testing.T) {
	dataset := Dataset{Kind: DatasetRetrieval, Version: "1", Retrieval: []RetrievalSample{
		{ID: "r1", TenantID: "t", Query: "q", ExpectedMemoryIDs: []string{"m1", "m2"}, ForbiddenMemoryIDs: []string{"evil"}},
		{ID: "r2", TenantID: "t", Query: "q2", ExpectedMemoryIDs: []string{"m3"}},
	}}
	report, err := EvaluateRetrieval(dataset, []RetrievalPrediction{
		{SampleID: "r1", RecalledIDs: []string{"m1", "evil"}, PromotedIDs: []string{"m1", "evil"}, LatencyMs: 100, Tokens: 200, CostUSD: 0.10, CacheHit: true},
		{SampleID: "r2", RecalledIDs: []string{"m3"}, PromotedIDs: []string{"m3"}, LatencyMs: 300, Tokens: 400, CostUSD: 0.30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.RecallPrecision != 2.0/3.0 {
		t.Fatalf("recall precision=%v", report.RecallPrecision)
	}
	if report.PromotionPrecision != 2.0/3.0 {
		t.Fatalf("promotion precision=%v", report.PromotionPrecision)
	}
	if report.WrongMemoryRate != 1.0/3.0 {
		t.Fatalf("wrong memory rate=%v", report.WrongMemoryRate)
	}
	if report.LatencyP95Ms != 300 || report.CacheHitRate != 0.5 || report.AverageCostUSD != 0.2 || report.TokensPerPrediction != 300 {
		t.Fatalf("report=%#v", report)
	}
	if _, err := EvaluateRetrieval(dataset, []RetrievalPrediction{{SampleID: "r1"}}); err == nil {
		t.Fatal("missing predictions accepted")
	}
}

func TestEvaluateFailureUsesAttributionNotConfidence(t *testing.T) {
	dataset := Dataset{Kind: DatasetFailure, Version: "1", Failure: []FailureSample{
		{ID: "f1", TenantID: "t", Transcript: "x", ExpectedConfidence: "confirmed", ExpectedAttribution: "direct"},
		{ID: "f2", TenantID: "t", Transcript: "y", ExpectedConfidence: "suspected", ExpectedAttribution: "unknown"},
	}}
	report, err := EvaluateFailure(dataset, []FailurePrediction{
		{SampleID: "f1", Confidence: "confirmed", Attribution: "direct"},
		{SampleID: "f2", Confidence: "suspected", Attribution: "correlated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AttributionAccuracy != 0.5 || report.FailureSamples != 2 {
		t.Fatalf("failure report=%#v", report)
	}
}

func trimNewline(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		return value[:len(value)-1]
	}
	return value
}
