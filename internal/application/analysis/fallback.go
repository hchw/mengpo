package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// RuleFallback provides deterministic event classifications and failure signals
// only. It never emits memory candidates because rules alone cannot create Stable Memory.
type RuleFallback struct{}

func (RuleFallback) Analyze(ctx context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.AnalystResult{}, err
	}
	result := ports.AnalystResult{}
	for _, event := range batch.Events {
		if err := ctx.Err(); err != nil {
			return ports.AnalystResult{}, err
		}
		category := "event"
		if event.SessionID != "" {
			category = "session_event"
		}
		result.Classifications = append(result.Classifications, ports.Classification{EventID: event.EventID, Category: category, Confidence: .5})
		var payload map[string]any
		_ = json.Unmarshal(event.Payload, &payload)
		if status, ok := payload["status"].(string); ok && (status == "error" || status == "failed" || status == "timeout") {
			result.Failures = append(result.Failures, ports.FailureAssessment{EventIDs: []string{event.EventID}, Conclusion: string(observation.FailureSuspected), Attribution: string(observation.AttributionInferred), Confidence: .45})
		}
	}
	return result, nil
}

type ResilientService struct {
	Primary  ports.MemoryAnalyst
	Fallback ports.MemoryAnalyst
}

func (s ResilientService) Analyze(ctx context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	var primaryErr error
	if s.Primary != nil {
		result, err := s.Primary.Analyze(ctx, batch)
		if err == nil {
			return result, nil
		}
		primaryErr = err
		if errors.Is(err, context.Canceled) {
			return ports.AnalystResult{}, err
		}

	}
	fallback := s.Fallback
	if fallback == nil {
		fallback = RuleFallback{}
	}
	fallbackCtx := ctx
	var fallbackCancel context.CancelFunc
	if errors.Is(primaryErr, context.DeadlineExceeded) {
		fallbackCtx, fallbackCancel = context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
		defer fallbackCancel()
	}
	result, err := fallback.Analyze(fallbackCtx, batch)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	// Defense in depth: fallback may not accidentally promote analysis to memory candidates.
	result.Candidates = nil
	result.Conflicts = nil
	result.Degraded = true
	result.DegradedReason = "rules_only"
	if errors.Is(primaryErr, context.DeadlineExceeded) {
		result.DegradedReason = "primary_timeout"
	}
	return result, nil
}

func NewRuleFallbackBatch(tenantID, runID string, events []ports.AnalysisEvent, now time.Time) ports.AnalysisBatch {
	return ports.AnalysisBatch{TenantID: tenantID, RunID: runID, Events: events, PromptVersion: "rules-only", SchemaVersion: "analysis-v1", Metadata: map[string]string{"degraded_mode": "rules_only", "generated_at": now.UTC().Format(time.RFC3339Nano)}}
}
