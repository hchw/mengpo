package observation

import (
	"encoding/json"
	"testing"
	"time"
)

func analysisEvent(messageType, payload string, source SourceType) Event {
	return Event{
		ID:          "11111111-1111-4111-8111-111111111111",
		TenantID:    "tenant-a",
		SourceType:  source,
		SourceID:    "agent-1",
		MessageType: messageType,
		Payload:     json.RawMessage(payload),
		OccurredAt:  time.Now().UTC(),
	}
}

func TestAnalysisTaskGating(t *testing.T) {
	cases := []struct {
		name     string
		event    Event
		wantTask string
		want     bool
	}{
		{
			name:  "ordinary message is not analyzed",
			event: analysisEvent("message", `{"text":"just chatting"}`, SourceUser),
		},
		{
			name:     "failure status triggers failure analysis",
			event:    analysisEvent("message", `{"status":"error"}`, SourceTool),
			wantTask: TaskFailureAnalysis,
			want:     true,
		},
		{
			name:     "explicit failure type triggers analysis",
			event:    analysisEvent("tool.failure", `{}`, SourceTool),
			wantTask: TaskFailureAnalysis,
			want:     true,
		},
		{
			name:     "user correction triggers consolidation",
			event:    analysisEvent("correction", `{}`, SourceUser),
			wantTask: TaskConsolidation,
			want:     true,
		},
		{
			name:     "explicit remember intent triggers consolidation",
			event:    analysisEvent("message", `{"remember":true,"text":"I prefer dark mode"}`, SourceUser),
			wantTask: TaskConsolidation,
			want:     true,
		},
		{
			name:     "memory_intent field triggers consolidation",
			event:    analysisEvent("message", `{"memory_intent":"forget","text":"drop that"}`, SourceUser),
			wantTask: TaskConsolidation,
			want:     true,
		},
		{
			name:     "context compaction triggers consolidation",
			event:    analysisEvent("context.compaction", `{"compaction":true}`, SourceWorkflow),
			wantTask: TaskConsolidation,
			want:     true,
		},
		{
			name:     "branch summary triggers consolidation",
			event:    analysisEvent("context.branch_summary", `{}`, SourceWorkflow),
			wantTask: TaskConsolidation,
			want:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, gated := AnalysisTask(tc.event)
			if gated != tc.want || task != tc.wantTask {
				t.Fatalf("AnalysisTask() = (%q, %v), want (%q, %v)", task, gated, tc.wantTask, tc.want)
			}
		})
	}
}

func TestHasMemoryIntentIgnoresNonUserSources(t *testing.T) {
	event := analysisEvent("message", `{"remember":true}`, SourceTool)
	if HasMemoryIntent(event) {
		t.Fatal("non-user sources must not carry explicit memory intent")
	}
}

func TestIsSessionBoundaryIsSourceAgnostic(t *testing.T) {
	if !IsSessionBoundary(analysisEvent("context.compaction", `{}`, SourceWorkflow)) {
		t.Fatal("compaction must be a session boundary regardless of source")
	}
	if IsSessionBoundary(analysisEvent("turn.outcome", `{}`, SourceAgent)) {
		t.Fatal("ordinary turn outcomes are not session boundaries")
	}
}
