package analysis

import (
	"encoding/json"
	"strings"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// DeterministicCandidate captures a memory that requires no inference at all:
// an explicit user instruction to remember, or a runtime session-boundary
// summary. It never infers, merges, or promotes; it only records what an event
// already states, so recall and injection stay functional without a Memory LLM.
//
// The dispatcher calls this when the configured analyst produced no candidate
// for the event, so a missing, disabled, or unreachable model does not silently
// drop explicit instructions or session summaries.
func DeterministicCandidate(event ports.AnalysisEvent) (ports.CandidateMemory, bool) {
	// Events without a session are skipped because a memory cannot be
	// attributed to an owner without one.
	if strings.TrimSpace(event.SessionID) == "" {
		return ports.CandidateMemory{}, false
	}
	text := eventText(event.Payload)
	if text == "" {
		return ports.CandidateMemory{}, false
	}
	switch {
	case carriesRemember(event):
		// An explicit instruction is durable and belongs to the user, not the
		// session; the evidence session is used only to resolve the owner.
		return ports.CandidateMemory{
			CandidateID:      "rule:remember:" + event.EventID,
			EvidenceEventIDs: []string{event.EventID},
			ScopeType:        "user-global",
			ScopeID:          event.SessionID,
			Content:          encodedContent(map[string]string{"text": text}),
			Confidence:       0.9,
		}, true
	case observation.IsSessionBoundary(observation.Event{MessageType: event.MessageType}):
		// A compaction or branch summary is the session's own working memory.
		return ports.CandidateMemory{
			CandidateID:      "rule:summary:" + event.EventID,
			EvidenceEventIDs: []string{event.EventID},
			ScopeType:        "session",
			ScopeID:          event.SessionID,
			Content:          encodedContent(map[string]string{"summary": text}),
			Confidence:       0.6,
		}, true
	}
	return ports.CandidateMemory{}, false
}

// carriesRemember reports whether the event is an explicit remember instruction.
// It matches the declared message type and the payload flag, mirroring the
// server-side memory-intent rule.
func carriesRemember(event ports.AnalysisEvent) bool {
	if strings.Contains(strings.ToLower(event.MessageType), "remember") {
		return true
	}
	var fields map[string]any
	if json.Unmarshal(event.Payload, &fields) != nil {
		return false
	}
	remember, ok := fields["remember"].(bool)
	return ok && remember
}

// eventText reads the carried text from either the raw payload or the
// normalized payload's wrapped data, so the immediate and scheduled paths agree.
func eventText(payload json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(payload, &fields) != nil {
		return ""
	}
	if text := firstString(fields); text != "" {
		return text
	}
	if data, ok := fields["data"].(map[string]any); ok {
		return firstString(data)
	}
	return ""
}

func firstString(fields map[string]any) string {
	for _, key := range []string{"text", "summary", "content", "message"} {
		if text, ok := fields[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func encodedContent(fields map[string]string) json.RawMessage {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return encoded
}
