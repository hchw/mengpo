package analysis

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

func validResult() ports.AnalystResult {
	return ports.AnalystResult{
		Classifications: []ports.Classification{{EventID: "event-1", Category: "tool_result", Confidence: .8}},
		Failures:        []ports.FailureAssessment{{EventIDs: []string{"event-1"}, Conclusion: "suspected_failure", Attribution: "inferred", Confidence: .5}},
		Candidates:      []ports.CandidateMemory{{CandidateID: "candidate-1", EvidenceEventIDs: []string{"event-1"}, ScopeType: "session", ScopeID: "session-1", Content: json.RawMessage(`{"summary":"x"}`), Confidence: .7}},
		Conflicts:       []ports.ConflictAssessment{{CandidateIDs: []string{"candidate-1"}, Conflicted: true, ReasonCode: "contradictory_evidence"}},
	}
}

func TestValidateResultAcceptsEvidenceBoundScopedOutput(t *testing.T) {
	batch := validBatch()
	batch.Events[0].SessionID = "session-1"
	if err := ValidateResult(batch, validResult()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateResultRejectsForeignEvidenceInvalidScopeAndConfidence(t *testing.T) {
	batch := validBatch()
	batch.Events[0].SessionID = "session-1"
	tests := map[string]func(*ports.AnalystResult){
		"foreign evidence":           func(r *ports.AnalystResult) { r.Candidates[0].EvidenceEventIDs = []string{"foreign"} },
		"cross session scope":        func(r *ports.AnalystResult) { r.Candidates[0].ScopeID = "session-2" },
		"unsupported scope":          func(r *ports.AnalystResult) { r.Candidates[0].ScopeType = "agent" },
		"invalid confidence":         func(r *ports.AnalystResult) { r.Candidates[0].Confidence = math.NaN() },
		"foreign conflict candidate": func(r *ports.AnalystResult) { r.Conflicts[0].CandidateIDs = []string{"foreign"} },
		"malformed output":           func(r *ports.AnalystResult) { r.Candidates[0].Content = json.RawMessage(`{`) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := validResult()
			mutate(&result)
			if err := ValidateResult(batch, result); !errors.Is(err, ErrInvalidAnalysisResult) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
