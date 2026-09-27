package memory

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestMemoryValidationAndJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)
	original := Memory{
		ID: "memory-1", IdempotencyKey: "run-1:candidate-1", UserID: "user-1",
		ScopeType: ScopeUserGlobal, ScopeID: "user-1", Type: "preference", Status: StatusCandidate,
		Visibility: VisibilityPrivate, Confidence: 0.82,
		Applicability: Applicability{Conditions: []string{"coding"}, Exclusions: []string{"medical"}, Attributes: map[string]string{"language": "go"}},
		Content:       json.RawMessage(`{"text":"prefers concise answers"}`), ContentText: "prefers concise answers",
		DefaultRetrieval: false, Version: 3,
		Provenance: Provenance{SourceEventIDs: []string{"event-1"}, RunID: "run-1", ModelVersion: "analyst-v2"},
		CreatedAt:  now, UpdatedAt: now,
	}
	if err := original.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded Memory
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", decoded, original)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["tenant_id"]; exists {
		t.Fatal("tenant isolation boundary must not become a memory scope field")
	}
}

func TestMemoryValidationRestrictsScopesAndConfidence(t *testing.T) {
	base := Memory{
		ID: "memory-1", IdempotencyKey: "key-1", UserID: "user-1",
		ScopeType: ScopeSession, ScopeID: "session-1", SessionID: "session-1",
		Type: "fact", Status: StatusActive, Confidence: 0.5,
		Content: json.RawMessage(`{"fact":true}`),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid session memory: %v", err)
	}
	invalid := base
	invalid.ScopeType = ScopeType("project")
	if err := invalid.Validate(); err == nil {
		t.Fatal("project scope must be rejected")
	}
	invalid = base
	invalid.Confidence = math.NaN()
	if err := invalid.Validate(); err == nil {
		t.Fatal("NaN confidence must be rejected")
	}
	invalid = base
	invalid.ScopeID = "another-session"
	if err := invalid.Validate(); err == nil {
		t.Fatal("scope/session mismatch must be rejected")
	}
}

func TestRelatedDomainTypesRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	ended := now.Add(time.Hour)
	tests := []struct {
		name  string
		value any
		new   func() any
	}{
		{
			name: "evidence",
			value: Evidence{ID: "evidence-1", MemoryID: "memory-1", RawEventID: "event-1", Role: "supports", Confidence: .9,
				Attribution: AttributionCorrelated, Metadata: json.RawMessage(`{"source":"tool"}`), CreatedAt: now},
			new: func() any { return &Evidence{} },
		},
		{
			name:  "relation",
			value: Relation{ID: "relation-1", SourceMemoryID: "memory-1", TargetMemoryID: "memory-2", Type: RelationSupports, Confidence: .7, CreatedAt: now},
			new:   func() any { return &Relation{} },
		},
		{
			name: "feedback",
			value: Feedback{ID: "feedback-1", MemoryID: "memory-1", UserID: "user-1", SessionID: "session-1",
				Type: FeedbackCorrected, Reason: "incorrect detail", RequestID: "request-1", CreatedAt: now},
			new: func() any { return &Feedback{} },
		},
		{
			name: "session",
			value: Session{ID: "session-1", UserID: "user-1", CreatedByAgent: "agent-1", Status: SessionClosed,
				Metadata: json.RawMessage(`{"source":"agent"}`), StartedAt: now, EndedAt: &ended, CreatedAt: now, UpdatedAt: ended},
			new: func() any { return &Session{} },
		},
		{
			name: "projection",
			value: Projection{ID: "projection-1", RequestID: "request-1", UserID: "user-1", SessionID: "session-1",
				Mode: RetrievalDiverge, Memories: []ProjectedMemory{{Memory: Memory{ID: "memory-1", Content: json.RawMessage(`{"text":"prior error"}`)}, SelectionReason: "repeated failure"}},
				OpenLoops: []string{"verify test result"}, Uncertainties: []string{"root cause unknown"},
				SelectionReasons: map[string]string{"mode": "divergence"}, Usage: ProjectionUsage{Candidates: 10, Ranked: 4, InjectedTokens: 120, InjectionBudget: 200},
				DegradedMode: "lexical-only", CreatedAt: now},
			new: func() any { return &Projection{} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			decoded := test.new()
			if err := json.Unmarshal(encoded, decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			original := reflect.ValueOf(test.value)
			actual := reflect.ValueOf(decoded).Elem()
			if !reflect.DeepEqual(actual.Interface(), original.Interface()) {
				t.Fatalf("round-trip mismatch: got %#v, want %#v", actual.Interface(), original.Interface())
			}
		})
	}
}
