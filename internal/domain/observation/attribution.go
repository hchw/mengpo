package observation

import "strings"

type AccessLevel string

const (
	Level0 AccessLevel = "level0"
	Level1 AccessLevel = "level1"
	Level2 AccessLevel = "level2"
)

type AttributionLevel string

const (
	AttributionDirect     AttributionLevel = "direct"
	AttributionCorrelated AttributionLevel = "correlated"
	AttributionInferred   AttributionLevel = "inferred"
	AttributionUnknown    AttributionLevel = "unknown"
)

type Trace struct {
	TaskID        string   `json:"task_id,omitempty"`
	AttemptID     string   `json:"attempt_id,omitempty"`
	ProjectionID  string   `json:"projection_id,omitempty"`
	UsedMemoryIDs []string `json:"used_memory_ids,omitempty"`
	ToolResultID  string   `json:"tool_result_id,omitempty"`
	OutcomeID     string   `json:"outcome_id,omitempty"`
	FeedbackID    string   `json:"feedback_id,omitempty"`
}

type Attribution struct {
	Level       AttributionLevel `json:"level"`
	Limitations []string         `json:"limitations,omitempty"`
}

// AssessAttribution measures linkage completeness without inferring missing edges.
//
// Direct attribution requires a session or conversation, one branch reference
// (parent event, task, attempt, or projection), and both a tool result and an
// outcome identifier on the same event; the remaining trace fields stay listed
// as limitations of the non-direct levels but are not fabricated requirements
// for direct attribution.
func AssessAttribution(sessionID, conversationID, parentEventID string, trace Trace) Attribution {
	missing := make([]string, 0, 6)
	for _, field := range []struct{ name, value string }{
		{"task_id", trace.TaskID}, {"attempt_id", trace.AttemptID}, {"projection_id", trace.ProjectionID},
		{"used_memory_ids", strings.Join(trace.UsedMemoryIDs, ",")}, {"tool_result_id", trace.ToolResultID}, {"outcome_id", trace.OutcomeID},
	} {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}
	hasSession := strings.TrimSpace(sessionID) != "" || strings.TrimSpace(conversationID) != ""
	hasBranch := strings.TrimSpace(parentEventID) != "" || trace.TaskID != "" || trace.AttemptID != "" || trace.ProjectionID != ""
	hasOutcomePair := strings.TrimSpace(trace.ToolResultID) != "" && strings.TrimSpace(trace.OutcomeID) != ""
	if hasSession && hasBranch && hasOutcomePair {
		return Attribution{Level: AttributionDirect}
	}
	if hasSession {
		if hasBranch || strings.TrimSpace(trace.ToolResultID) != "" || strings.TrimSpace(trace.OutcomeID) != "" || strings.TrimSpace(trace.FeedbackID) != "" {
			return Attribution{Level: AttributionCorrelated, Limitations: missing}
		}
		return Attribution{Level: AttributionInferred, Limitations: missing}
	}
	return Attribution{Level: AttributionUnknown, Limitations: append([]string{"session_or_conversation_id"}, missing...)}
}
