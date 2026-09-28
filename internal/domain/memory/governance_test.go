package memory

import (
	"encoding/json"
	"errors"
	"testing"
)

func governanceCandidate() Memory {
	return Memory{
		ID: "memory-1", IdempotencyKey: "run-1:memory-1", UserID: "user-1",
		ScopeType: ScopeUserGlobal, ScopeID: "user-1", Type: "preference",
		Status: StatusCandidate, Confidence: 0.2, Applicability: Applicability{Conditions: []string{"coding"}},
		Content: json.RawMessage(`{"text":"prefers concise answers"}`), DefaultRetrieval: false,
	}
}

func highDirectEvidence(id, sessionID string, confidence float64) Evidence {
	return Evidence{
		ID: "evidence-" + id, MemoryID: "memory-1", RawEventID: id, SourceSessionID: sessionID,
		Role: "supports", Confidence: confidence, Reliability: ReliabilityHigh, Attribution: AttributionDirect,
	}
}

func TestGovernanceLeavesUnverifiedCandidateUnpromoted(t *testing.T) {
	decision, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{}, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusCandidate || decision.Memory.DefaultRetrieval {
		t.Fatalf("unverified candidate was promoted or made retrievable: %#v", decision.Memory)
	}
	if decision.Reason != "evidence_insufficient_for_promotion" {
		t.Fatalf("decision reason = %q", decision.Reason)
	}
}

func TestGovernancePromotesWithVerifiedDirectEvidence(t *testing.T) {
	input := GovernanceInput{VerifiedEvidence: []Evidence{highDirectEvidence("event-1", "session-1", 0.75)}}
	decision, err := EvaluateGovernance(governanceCandidate(), input, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusActive || !decision.Memory.DefaultRetrieval || decision.VerifiedEvents != 1 {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestGovernanceRequiresIndependentSessionsForStablePromotion(t *testing.T) {
	memory := governanceCandidate()
	memory.Status = StatusActive
	memory.Confidence = 0.75
	memory.DefaultRetrieval = true
	input := GovernanceInput{VerifiedEvidence: []Evidence{
		highDirectEvidence("event-1", "session-1", 0.7),
		highDirectEvidence("event-2", "session-2", 0.7),
	}}
	decision, err := EvaluateGovernance(memory, input, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusStable || !decision.Memory.DefaultRetrieval || decision.VerifiedEvents != 2 {
		t.Fatalf("independently corroborated memory was not stabilized: %#v", decision)
	}
}

func TestGovernanceDoesNotPromoteFromCorrelatedOrWeakEvidence(t *testing.T) {
	evidence := highDirectEvidence("event-1", "session-1", 0.95)
	evidence.Attribution = AttributionInferred
	decision, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: []Evidence{evidence}}, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusCandidate || decision.Memory.DefaultRetrieval {
		t.Fatalf("inferred evidence promoted a candidate: %#v", decision.Memory)
	}
}

func TestUserConfirmationConflictRejectionAndExpirationDriveGovernance(t *testing.T) {
	tests := []struct {
		name   string
		input  GovernanceInput
		status MemoryStatus
		active bool
	}{
		{name: "confirmed", input: GovernanceInput{ConfirmByUser: true}, status: StatusStable, active: true},
		{name: "rejected", input: GovernanceInput{RejectByUser: true}, status: StatusRejected},
		{name: "conflicted", input: GovernanceInput{ConflictDetected: true}, status: StatusConflicted},
		{name: "expired", input: GovernanceInput{Expired: true}, status: StatusExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := EvaluateGovernance(governanceCandidate(), test.input, DefaultGovernancePolicy())
			if err != nil {
				t.Fatalf("EvaluateGovernance() error = %v", err)
			}
			if decision.Memory.Status != test.status || decision.Memory.DefaultRetrieval != test.active {
				t.Fatalf("decision = %#v, want status %q/retrievable %t", decision.Memory, test.status, test.active)
			}
			if test.name == "confirmed" && decision.Memory.Confidence != 1 {
				t.Fatalf("user-confirmed confidence = %v, want 1", decision.Memory.Confidence)
			}
		})
	}
}

func TestGovernanceRejectsInvalidEvidenceAndAmbiguousSignals(t *testing.T) {
	badEvidence := highDirectEvidence("event-1", "session-1", 0.9)
	badEvidence.MemoryID = "another-memory"
	if _, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: []Evidence{badEvidence}}, DefaultGovernancePolicy()); !errors.Is(err, ErrInvalidGovernanceInput) {
		t.Fatalf("cross-memory evidence error = %v, want ErrInvalidGovernanceInput", err)
	}
	if _, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{ConfirmByUser: true, RejectByUser: true}, DefaultGovernancePolicy()); !errors.Is(err, ErrInvalidGovernanceInput) {
		t.Fatalf("ambiguous governance signals error = %v, want ErrInvalidGovernanceInput", err)
	}
	first := highDirectEvidence("same-event", "session-1", 0.8)
	second := highDirectEvidence("same-event", "session-2", 0.8)
	if _, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: []Evidence{first, second}}, DefaultGovernancePolicy()); !errors.Is(err, ErrInvalidGovernanceInput) {
		t.Fatalf("event attached to multiple sessions error = %v, want ErrInvalidGovernanceInput", err)
	}
}

func TestApplicabilityUsesExactConditionsExclusionsAndAttributes(t *testing.T) {
	applicability := Applicability{
		Conditions: []string{"coding"}, Exclusions: []string{"production"},
		Attributes: map[string]string{"language": "go"},
	}
	if !applicability.AppliesTo(map[string]string{"topic": "coding", "language": "go"}) {
		t.Fatal("matching context should satisfy applicability")
	}
	if applicability.AppliesTo(map[string]string{"topic": "coding", "language": "go", "environment": "production"}) {
		t.Fatal("excluded context must not satisfy applicability")
	}
	if applicability.AppliesTo(map[string]string{"topic": "coding", "language": "rust"}) {
		t.Fatal("attribute mismatch must not satisfy applicability")
	}
	if err := (Applicability{Conditions: []string{"coding"}, Exclusions: []string{"coding"}}).Validate(); err == nil {
		t.Fatal("contradictory applicability must be rejected")
	}
}
