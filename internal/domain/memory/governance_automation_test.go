package memory

import "testing"

func reliabilityEvidence(id, sessionID string, confidence float64, attribution Attribution) Evidence {
	return Evidence{
		ID: "evidence-" + id, MemoryID: "memory-1", RawEventID: id, SourceSessionID: sessionID,
		Role: "supports", Confidence: confidence, Reliability: ReliabilityHigh, Attribution: attribution,
	}
}

func TestRecurrenceActivatesAcrossIndependentSessions(t *testing.T) {
	// Tool-result style evidence is high reliability but only loosely linked
	// (correlated), so a single event cannot activate it. Recurrence across
	// three independent sessions can.
	evidence := []Evidence{
		reliabilityEvidence("e1", "session-1", 0.8, AttributionCorrelated),
		reliabilityEvidence("e2", "session-2", 0.8, AttributionCorrelated),
		reliabilityEvidence("e3", "session-3", 0.8, AttributionCorrelated),
	}
	decision, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: evidence}, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusActive || decision.Reason != "independent_session_recurrence" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRecurrenceNeedsEnoughSessions(t *testing.T) {
	// Two sessions corroborate stable, but recurrence needs three to activate
	// a loosely-linked claim.
	evidence := []Evidence{
		reliabilityEvidence("e1", "session-1", 0.9, AttributionCorrelated),
		reliabilityEvidence("e2", "session-2", 0.9, AttributionCorrelated),
	}
	decision, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: evidence}, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusCandidate {
		t.Fatalf("two sessions promoted a loosely-linked candidate: %#v", decision)
	}
}

func TestUnknownReliabilityNeverPromotes(t *testing.T) {
	evidence := []Evidence{
		{ID: "evidence-1", MemoryID: "memory-1", RawEventID: "e1", SourceSessionID: "session-1", Role: "supports", Confidence: 1, Reliability: ReliabilityUnknown, Attribution: AttributionInferred},
		{ID: "evidence-2", MemoryID: "memory-1", RawEventID: "e2", SourceSessionID: "session-2", Role: "supports", Confidence: 1, Reliability: ReliabilityUnknown, Attribution: AttributionInferred},
		{ID: "evidence-3", MemoryID: "memory-1", RawEventID: "e3", SourceSessionID: "session-3", Role: "supports", Confidence: 1, Reliability: ReliabilityUnknown, Attribution: AttributionInferred},
	}
	decision, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: evidence}, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusCandidate || decision.Memory.DefaultRetrieval {
		t.Fatalf("unknown reliability promoted a candidate: %#v", decision)
	}
}

func TestDirectEvidenceActivatesSingleSession(t *testing.T) {
	decision, err := EvaluateGovernance(governanceCandidate(), GovernanceInput{VerifiedEvidence: []Evidence{highDirectEvidence("e1", "session-1", 0.9)}}, DefaultGovernancePolicy())
	if err != nil {
		t.Fatalf("EvaluateGovernance() error = %v", err)
	}
	if decision.Memory.Status != StatusActive || decision.Reason != "verified_direct_evidence" {
		t.Fatalf("decision = %#v", decision)
	}
}
