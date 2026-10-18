package analysis

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func ruleBatch(events ...ports.AnalysisEvent) ports.AnalysisBatch {
	return ports.AnalysisBatch{TenantID: "tenant-a", RunID: "run-1", SchemaVersion: "schema-v1", Events: events}
}

func TestDeterministicCandidateCapturesExplicitRemember(t *testing.T) {
	event := ports.AnalysisEvent{
		EventID:     "e1",
		SessionID:   "session-1",
		MessageType: "user_remember",
		OccurredAt:  time.Now().UTC(),
		Payload:     json.RawMessage(`{"remember":true,"text":"always set GOPROXY"}`),
	}
	candidate, ok := DeterministicCandidate(event)
	if !ok {
		t.Fatal("explicit remember was not captured")
	}
	if candidate.ScopeType != "user-global" {
		t.Fatalf("remember scope = %q, want user-global", candidate.ScopeType)
	}
	if string(candidate.Content) != `{"text":"always set GOPROXY"}` {
		t.Fatalf("remember content = %s", candidate.Content)
	}
	if err := ValidateResult(ruleBatch(event), ports.AnalystResult{Candidates: []ports.CandidateMemory{candidate}}); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}
}

func TestDeterministicCandidateCapturesSessionBoundary(t *testing.T) {
	event := ports.AnalysisEvent{
		EventID:     "e2",
		SessionID:   "session-1",
		MessageType: "context.compaction",
		OccurredAt:  time.Now().UTC(),
		Payload:     json.RawMessage(`{"context_summary":true,"summary":"the build needs GOPROXY"}`),
	}
	candidate, ok := DeterministicCandidate(event)
	if !ok {
		t.Fatal("session boundary was not captured")
	}
	if candidate.ScopeType != "session" || candidate.ScopeID != "session-1" {
		t.Fatalf("boundary candidate = %+v", candidate)
	}
	if string(candidate.Content) != `{"summary":"the build needs GOPROXY"}` {
		t.Fatalf("boundary content = %s", candidate.Content)
	}
	if candidate.MemoryType != ports.MemoryTypeSessionSummary {
		t.Fatalf("boundary memory type = %q, want %q", candidate.MemoryType, ports.MemoryTypeSessionSummary)
	}
	if err := ValidateResult(ruleBatch(event), ports.AnalystResult{Candidates: []ports.CandidateMemory{candidate}}); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}
}

func TestDeterministicCandidateReadsNormalizedPayloadText(t *testing.T) {
	event := ports.AnalysisEvent{
		EventID:     "e3",
		SessionID:   "session-1",
		MessageType: "context.branch_summary",
		OccurredAt:  time.Now().UTC(),
		Payload:     json.RawMessage(`{"source_type":"workflow","message_type":"context.branch_summary","data":{"summary":"wrapped text"}}`),
	}
	candidate, ok := DeterministicCandidate(event)
	if !ok || string(candidate.Content) != `{"summary":"wrapped text"}` {
		t.Fatalf("normalized payload candidate = %+v ok=%v", candidate, ok)
	}
}

func TestDeterministicCandidateCapturesSessionlessRemember(t *testing.T) {
	event := ports.AnalysisEvent{
		EventID:     "e1",
		UserID:      "00000000-0000-4000-8000-0000000000f1",
		SessionID:   "",
		MessageType: "user_remember",
		OccurredAt:  time.Now().UTC(),
		Payload:     json.RawMessage(`{"remember":true,"text":"always set GOPROXY"}`),
	}
	candidate, ok := DeterministicCandidate(event)
	if !ok {
		t.Fatal("a sessionless explicit remember must still be captured")
	}
	if candidate.ScopeType != "user-global" {
		t.Fatalf("remember scope = %q, want user-global", candidate.ScopeType)
	}
	if candidate.ScopeID != event.UserID {
		t.Fatalf("remember scope id = %q, want the event owner %q", candidate.ScopeID, event.UserID)
	}
}

func TestDeterministicCandidateSkipsUnqualifiedEvents(t *testing.T) {
	cases := []struct {
		name  string
		event ports.AnalysisEvent
	}{
		{name: "no text", event: ports.AnalysisEvent{EventID: "e2", SessionID: "s", MessageType: "user_remember", Payload: json.RawMessage(`{"remember":true}`)}},
		{name: "ordinary", event: ports.AnalysisEvent{EventID: "e3", SessionID: "s", MessageType: "turn.outcome", Payload: json.RawMessage(`{"text":"just chatting"}`)}},
		{name: "sessionless boundary summary", event: ports.AnalysisEvent{EventID: "e4", SessionID: "", MessageType: "context.compaction", Payload: json.RawMessage(`{"context_summary":true,"summary":"session memory needs a session"}`)}},
		{name: "empty boundary summary", event: ports.AnalysisEvent{EventID: "e5", SessionID: "s", MessageType: "context.compaction", Payload: json.RawMessage(`{"context_summary":true,"summary":"   "}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if candidate, ok := DeterministicCandidate(tc.event); ok {
				t.Fatalf("captured unqualified event: %+v", candidate)
			}
		})
	}
}
