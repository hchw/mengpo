package evaluation

import (
	"context"
	"errors"
	"testing"
)

type fakePredictor struct {
	retrieval map[string]RetrievalPrediction
	failure   map[string]FailurePrediction
	errOn     string
}

func (f *fakePredictor) PredictRetrieval(_ context.Context, sample RetrievalSample) (RetrievalPrediction, error) {
	if f.errOn == sample.ID {
		return RetrievalPrediction{}, errors.New("predictor offline")
	}
	return f.retrieval[sample.ID], nil
}

func (f *fakePredictor) PredictFailure(_ context.Context, sample FailureSample) (FailurePrediction, error) {
	if f.errOn == sample.ID {
		return FailurePrediction{}, errors.New("predictor offline")
	}
	return f.failure[sample.ID], nil
}

func TestRunRetrievalTaskProducesReport(t *testing.T) {
	dataset, err := LoadDataset(DatasetRetrieval, mustOpen(t, "testdata/retrieval.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	predictor := &fakePredictor{retrieval: map[string]RetrievalPrediction{
		"r1": {RecalledIDs: []string{"m1"}, PromotedIDs: []string{"m1"}, LatencyMs: 50, Tokens: 100, CostUSD: 0.01, CacheHit: true},
		"r2": {RecalledIDs: []string{"m2"}, PromotedIDs: []string{}, LatencyMs: 70, Tokens: 120, CostUSD: 0.02},
	}}
	report, err := Run(context.Background(), dataset, predictor)
	if err != nil {
		t.Fatal(err)
	}
	if report.RetrievalSamples != 2 || report.WrongMemoryRate != 0 || report.CacheHitRate != 0.5 {
		t.Fatalf("report=%#v", report)
	}
}

func TestRunFailureTaskAndPredictorFailure(t *testing.T) {
	dataset, err := LoadDataset(DatasetFailure, mustOpen(t, "testdata/failure.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	predictor := &fakePredictor{failure: map[string]FailurePrediction{
		"f1": {Confidence: "confirmed", Attribution: "direct"},
		"f2": {Confidence: "suspected", Attribution: "unknown"},
	}}
	report, err := Run(context.Background(), dataset, predictor)
	if err != nil || report.AttributionAccuracy != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := Run(context.Background(), dataset, &fakePredictor{errOn: "f2"}); err == nil {
		t.Fatal("predictor error was not surfaced")
	}
	if _, err := Run(context.Background(), Dataset{Kind: DatasetConsolidation, Version: "1"}, predictor); err == nil {
		t.Fatal("consolidation dataset accepted by Run")
	}
}
