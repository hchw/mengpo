package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

type failingAnalyst struct{ err error }

func (f failingAnalyst) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	return ports.AnalystResult{}, f.err
}

type maliciousFallback struct{}

func (maliciousFallback) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	return ports.AnalystResult{Candidates: []ports.CandidateMemory{{CandidateID: "should-not-survive"}}, Conflicts: []ports.ConflictAssessment{{CandidateIDs: []string{"should-not-survive"}}}}, nil
}

func TestResilientServiceRulesFallbackNeverCreatesStableCandidates(t *testing.T) {
	batch := validBatch()
	batch.Events[0].Payload = json.RawMessage(`{"status":"failed"}`)
	service := ResilientService{Primary: failingAnalyst{err: errors.New("provider timeout")}}
	result, err := service.Analyze(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || len(result.Conflicts) != 0 {
		t.Fatalf("fallback promoted outputs: %#v", result)
	}
	if len(result.Failures) != 1 || result.Failures[0].Conclusion != "suspected_failure" || result.Failures[0].Confidence >= .65 {
		t.Fatalf("fallback failure=%#v", result.Failures)
	}
}

func TestResilientServiceChecksContextAndStripsFallbackPromotion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (RuleFallback{}).Analyze(ctx, validBatch()); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error=%v", err)
	}
	service := ResilientService{Primary: failingAnalyst{err: errors.New("offline")}, Fallback: maliciousFallback{}}
	result, err := service.Analyze(context.Background(), validBatch())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || len(result.Conflicts) != 0 {
		t.Fatalf("unsafe fallback result=%#v", result)
	}
}

type timeoutAnalyst struct{}

func (timeoutAnalyst) Analyze(ctx context.Context, _ ports.AnalysisBatch) (ports.AnalystResult, error) {
	<-ctx.Done()
	return ports.AnalystResult{}, ctx.Err()
}
func TestProviderTimeoutFallsBackToRules(t *testing.T) {
	batch := validBatch()
	batch.Events[0].Payload = json.RawMessage(`{"status":"failed"}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	result, err := (ResilientService{Primary: timeoutAnalyst{}}).Analyze(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 {
		t.Fatal("timeout fallback created memory candidate")
	}
	if len(result.Failures) != 1 {
		t.Fatalf("failures=%#v", result.Failures)
	}
}
