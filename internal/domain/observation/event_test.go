package observation

import (
	"encoding/json"
	"testing"
	"time"
)

func validEvent() Event {
	sequence := int64(2)
	return Event{ID: "event-1", TenantID: "tenant-1", IdempotencyKey: "key-1", SourceType: SourceTool,
		SourceID: "tool-1", MessageType: "tool.result", Payload: json.RawMessage(`{"ok":true}`),
		Sequence: &sequence, OccurredAt: time.Now(), Visibility: VisibilitySession,
		Reliability: ReliabilityHigh, RetentionClass: "standard"}
}

func TestEventValidateAcceptsCompleteObservation(t *testing.T) {
	if err := validEvent().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEventValidateRejectsInvalidFields(t *testing.T) {
	tests := map[string]func(*Event){
		"missing source":      func(e *Event) { e.SourceType = "unknown" },
		"invalid reliability": func(e *Event) { e.Reliability = "certain" },
		"invalid visibility":  func(e *Event) { e.Visibility = "global" },
		"invalid payload":     func(e *Event) { e.Payload = json.RawMessage(`{`) },
		"negative sequence":   func(e *Event) { n := int64(-1); e.Sequence = &n },
		"missing idempotency": func(e *Event) { e.IdempotencyKey = " " },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}
