package observation

import (
	"encoding/json"
	"testing"
	"time"
)

func signalEvent(id, messageType, sourceType string, payload string) Event {
	return Event{ID: id, IdempotencyKey: id, TenantID: "t", SessionID: "s", SourceType: SourceType(sourceType), SourceID: "tool", MessageType: messageType, Payload: json.RawMessage(payload), OccurredAt: time.Now(), Visibility: VisibilitySession, Reliability: ReliabilityHigh, RetentionClass: "test"}
}

func TestDetectFailureConfidenceWithoutOverclaiming(t *testing.T) {
	confirmed := DetectSignals(signalEvent("1", "tool.result", "tool", `{"success":false}`))
	if len(confirmed) != 1 || confirmed[0].Confidence != FailureConfirmed {
		t.Fatalf("signals=%#v", confirmed)
	}
	suspected := DetectSignals(signalEvent("2", "tool.error", "tool", `{}`))
	if len(suspected) != 1 || suspected[0].Confidence != FailureSuspected {
		t.Fatalf("signals=%#v", suspected)
	}
	if got := DetectSignals(signalEvent("3", "tool.result", "tool", `{"success":true}`)); len(got) != 0 {
		t.Fatalf("success generated failures: %#v", got)
	}
}

func TestDetectRetryAndUserCorrection(t *testing.T) {
	first := signalEvent("1", "tool.result", "tool", `{"status":"failed"}`)
	second := signalEvent("2", "tool.result", "tool", `{"status":"failed"}`)
	if signal, ok := DetectRetry(first, second); !ok || signal.Kind != SignalRetry || len(signal.EventIDs) != 2 {
		t.Fatalf("retry=%#v ok=%v", signal, ok)
	}
	correction := signalEvent("3", "feedback", "user", `{"corrected":true}`)
	if signal, ok := DetectUserCorrection(correction); !ok || signal.Confidence != FailureConfirmed {
		t.Fatalf("correction=%#v ok=%v", signal, ok)
	}
	if _, ok := DetectUserCorrection(first); ok {
		t.Fatal("tool event treated as user correction")
	}
}

func TestAggregateSignalsDeduplicatesEventIDs(t *testing.T) {
	a := Signal{Kind: SignalFailure, Confidence: FailureSuspected, AggregateKey: "session", EventIDs: []string{"a"}}
	b := Signal{Kind: SignalFailure, Confidence: FailureConfirmed, AggregateKey: "session", EventIDs: []string{"a", "b"}}
	result := AggregateSignals([]Signal{a, b})
	if len(result) != 1 || len(result[0].EventIDs) != 2 || result[0].Confidence != FailureConfirmed {
		t.Fatalf("aggregate=%#v", result)
	}
}
