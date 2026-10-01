package analysis

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidAnalysisResult = errors.New("analysis result failed evidence, scope, or confidence validation")

// ValidateResult verifies model output against the exact tenant batch that was analyzed.
func ValidateResult(batch ports.AnalysisBatch, result ports.AnalystResult) error {
	if batch.TenantID == "" || batch.RunID == "" || len(batch.Events) == 0 {
		return ErrInvalidBatch
	}
	events := make(map[string]ports.AnalysisEvent, len(batch.Events))
	for _, event := range batch.Events {
		if event.EventID == "" {
			return ErrInvalidAnalysisResult
		}
		events[event.EventID] = event
	}
	for _, item := range result.Classifications {
		if _, ok := events[item.EventID]; !ok || item.Category == "" || !unit(item.Confidence) {
			return ErrInvalidAnalysisResult
		}
	}
	for _, failure := range result.Failures {
		if len(failure.EventIDs) == 0 || !unit(failure.Confidence) || failure.Conclusion == "" {
			return ErrInvalidAnalysisResult
		}
		for _, id := range failure.EventIDs {
			if _, ok := events[id]; !ok {
				return ErrInvalidAnalysisResult
			}
		}
	}
	candidateIDs := make(map[string]struct{}, len(result.Candidates))
	for _, candidate := range result.Candidates {
		if candidate.CandidateID == "" || candidate.ScopeID == "" || len(candidate.EvidenceEventIDs) == 0 || !unit(candidate.Confidence) || len(candidate.Content) == 0 || !json.Valid(candidate.Content) {
			return ErrInvalidAnalysisResult
		}
		if candidate.ScopeType != "session" && candidate.ScopeType != "user-global" {
			return ErrInvalidAnalysisResult
		}
		if _, exists := candidateIDs[candidate.CandidateID]; exists {
			return ErrInvalidAnalysisResult
		}
		candidateIDs[candidate.CandidateID] = struct{}{}
		matchedSession := false
		for _, id := range candidate.EvidenceEventIDs {
			event, ok := events[id]
			if !ok {
				return ErrInvalidAnalysisResult
			}
			if candidate.ScopeType == "session" && event.SessionID == candidate.ScopeID {
				matchedSession = true
			}
		}
		if candidate.ScopeType == "session" && !matchedSession {
			return ErrInvalidAnalysisResult
		}
	}
	for _, conflict := range result.Conflicts {
		if len(conflict.CandidateIDs) == 0 {
			return ErrInvalidAnalysisResult
		}
		for _, id := range conflict.CandidateIDs {
			if _, ok := candidateIDs[id]; !ok {
				return ErrInvalidAnalysisResult
			}
		}
		if conflict.Conflicted && conflict.ReasonCode == "" {
			return ErrInvalidAnalysisResult
		}
	}
	return nil
}

func unit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
