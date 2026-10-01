package observation

import (
	"encoding/json"
	"strings"
)

type FailureConfidence string

const (
	FailureConfirmed FailureConfidence = "confirmed_failure"
	FailureInferred  FailureConfidence = "inferred_failure"
	FailureSuspected FailureConfidence = "suspected_failure"
	FailureUnknown   FailureConfidence = "unknown"
)

type SignalKind string

const (
	SignalFailure        SignalKind = "failure"
	SignalRetry          SignalKind = "retry"
	SignalUserCorrection SignalKind = "user_correction"
)

type Signal struct {
	Kind         SignalKind        `json:"kind"`
	Confidence   FailureConfidence `json:"confidence"`
	Rule         string            `json:"rule"`
	EventIDs     []string          `json:"event_ids"`
	SessionID    string            `json:"session_id,omitempty"`
	AggregateKey string            `json:"aggregate_key"`
}

// DetectSignals uses explicit event types/fields and does not claim tool failures
// as confirmed without an explicit failure outcome.
func DetectSignals(event Event) []Signal {
	payload := map[string]any{}
	_ = json.Unmarshal(event.Payload, &payload)
	kind := strings.ToLower(event.MessageType)
	failure := false
	confidence := FailureUnknown
	rule := ""
	if truthy(payload["success"]) == false && has(payload, "success") {
		failure, confidence, rule = true, FailureConfirmed, "explicit_success_false"
	} else if value, ok := payload["status"].(string); ok && isFailureStatus(value) {
		failure, confidence, rule = true, FailureConfirmed, "explicit_failure_status"
	} else if strings.Contains(kind, "error") || strings.Contains(kind, "failure") || strings.Contains(kind, "failed") {
		failure, confidence, rule = true, FailureSuspected, "failure_event_type"
	}
	if !failure {
		return nil
	}
	return []Signal{{Kind: SignalFailure, Confidence: confidence, Rule: rule, EventIDs: []string{event.ID}, SessionID: event.SessionID, AggregateKey: aggregateKey(event)}}
}

func DetectRetry(previous, current Event) (Signal, bool) {
	if previous.SessionID == "" || previous.SessionID != current.SessionID || previous.SourceID != current.SourceID {
		return Signal{}, false
	}
	if previous.MessageType != current.MessageType || (previous.SourceEventID != "" && previous.SourceEventID == current.SourceEventID) {
		return Signal{}, false
	}
	if !sameFailure(previous) || !sameFailure(current) {
		return Signal{}, false
	}
	return Signal{Kind: SignalRetry, Confidence: FailureCorrelatedConfidence(previous, current), Rule: "repeated_failure_same_source", EventIDs: []string{previous.ID, current.ID}, SessionID: current.SessionID, AggregateKey: aggregateKey(current)}, true
}

func DetectUserCorrection(event Event) (Signal, bool) {
	if event.SourceType != SourceUser {
		return Signal{}, false
	}
	kind := strings.ToLower(event.MessageType)
	payload := map[string]any{}
	_ = json.Unmarshal(event.Payload, &payload)
	if !strings.Contains(kind, "correct") && !strings.Contains(kind, "feedback") && !truthy(payload["corrected"]) {
		return Signal{}, false
	}
	return Signal{Kind: SignalUserCorrection, Confidence: FailureConfirmed, Rule: "explicit_user_correction", EventIDs: []string{event.ID}, SessionID: event.SessionID, AggregateKey: aggregateKey(event)}, true
}

func AggregateSignals(signals []Signal) []Signal {
	groups := map[string]Signal{}
	for _, signal := range signals {
		key := string(signal.Kind) + ":" + signal.AggregateKey
		current, exists := groups[key]
		if !exists {
			groups[key] = signal
			continue
		}
		ids := map[string]bool{}
		for _, id := range current.EventIDs {
			ids[id] = true
		}
		for _, id := range signal.EventIDs {
			if !ids[id] {
				current.EventIDs = append(current.EventIDs, id)
				ids[id] = true
			}
		}
		if confidenceRank(signal.Confidence) > confidenceRank(current.Confidence) {
			current.Confidence = signal.Confidence
		}
		groups[key] = current
	}
	result := make([]Signal, 0, len(groups))
	for _, signal := range groups {
		result = append(result, signal)
	}
	return result
}

func sameFailure(event Event) bool {
	for _, signal := range DetectSignals(event) {
		if signal.Kind == SignalFailure {
			return true
		}
	}
	return false
}
func FailureCorrelatedConfidence(a, b Event) FailureConfidence {
	for _, x := range DetectSignals(a) {
		for _, y := range DetectSignals(b) {
			if x.Confidence == FailureConfirmed && y.Confidence == FailureConfirmed {
				return FailureInferred
			}
		}
	}
	return FailureSuspected
}
func aggregateKey(event Event) string {
	if event.SessionID != "" {
		return event.SessionID + ":" + event.SourceID + ":" + event.MessageType
	}
	if event.ConversationID != "" {
		return event.ConversationID + ":" + event.SourceID + ":" + event.MessageType
	}
	return event.ID
}
func truthy(value any) bool                      { b, ok := value.(bool); return ok && b }
func has(values map[string]any, key string) bool { _, ok := values[key]; return ok }
func isFailureStatus(status string) bool {
	switch strings.ToLower(status) {
	case "error", "failed", "failure", "timeout", "timed_out":
		return true
	}
	return false
}
func confidenceRank(level FailureConfidence) int {
	switch level {
	case FailureConfirmed:
		return 4
	case FailureInferred:
		return 3
	case FailureSuspected:
		return 2
	default:
		return 1
	}
}

// Analysis task identifiers handed to the Memory LLM. A gated event maps to
// exactly one task so the prompt matches the intent.
const (
	TaskClassification  = "classify_event"
	TaskFailureAnalysis = "analyze_failure"
	TaskConsolidation   = "consolidate_memory"
	TaskConflictScan    = "analyze_conflict"
)

var memoryIntentMarkers = []string{"remember", "forget", "correct", "preference", "conflict", "memory_intent"}

// HasMemoryIntent reports whether the event carries an explicit instruction to
// change long-term memory. It is a local, cheap rule: no model is involved.
func HasMemoryIntent(event Event) bool {
	kind := strings.ToLower(event.MessageType)
	for _, marker := range memoryIntentMarkers {
		if strings.Contains(kind, marker) {
			return true
		}
	}
	if event.SourceType != SourceUser {
		return false
	}
	payload := map[string]any{}
	_ = json.Unmarshal(event.Payload, &payload)
	if truthy(payload["remember"]) || truthy(payload["forget"]) {
		return true
	}
	if intent, ok := payload["memory_intent"].(string); ok && strings.TrimSpace(intent) != "" {
		return true
	}
	return false
}

// AnalysisTask pre-screens an event using local rules only. It returns the task
// the Memory LLM should run and whether analysis is required at all. Ordinary
// events return ("", false): they are normalized but never sent to the model.
func AnalysisTask(event Event) (string, bool) {
	for _, signal := range DetectSignals(event) {
		if signal.Kind == SignalFailure {
			return TaskFailureAnalysis, true
		}
	}
	if _, ok := DetectUserCorrection(event); ok {
		return TaskConsolidation, true
	}
	if HasMemoryIntent(event) {
		return TaskConsolidation, true
	}
	return "", false
}
