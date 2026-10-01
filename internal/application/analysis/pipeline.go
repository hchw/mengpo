package analysis

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidPipelineInput = errors.New("invalid normalization pipeline input")

type RawEvent struct {
	ID            string
	TenantID      string
	SessionID     string
	OccurredAt    time.Time
	SourceType    string
	MessageType   string
	Payload       json.RawMessage
	Sequence      *int64
	ParentEventID string
}

type NormalizedEvent struct {
	ID                   string          `json:"id"`
	RawEventID           string          `json:"raw_event_id"`
	SchemaVersion        string          `json:"schema_version"`
	NormalizationVersion string          `json:"normalization_version"`
	TenantID             string          `json:"tenant_id"`
	SessionID            string          `json:"session_id,omitempty"`
	Sequence             *int64          `json:"sequence,omitempty"`
	ParentEventID        string          `json:"parent_event_id,omitempty"`
	Payload              json.RawMessage `json:"normalized_payload"`
	CreatedAt            time.Time       `json:"created_at"`
}

type SessionSummary struct {
	TenantID   string   `json:"tenant_id"`
	SessionID  string   `json:"session_id"`
	EventIDs   []string `json:"event_ids"`
	Text       string   `json:"text"`
	Confidence float64  `json:"confidence"`
}

type FailureCandidate struct {
	TenantID    string   `json:"tenant_id"`
	SessionID   string   `json:"session_id,omitempty"`
	EventIDs    []string `json:"event_ids"`
	Conclusion  string   `json:"conclusion"`
	Attribution string   `json:"attribution"`
	Confidence  float64  `json:"confidence"`
}

type PipelineResult struct {
	Normalized []NormalizedEvent       `json:"normalized_events"`
	Summaries  []SessionSummary        `json:"session_summaries"`
	Candidates []ports.CandidateMemory `json:"memory_candidates"`
	Failures   []FailureCandidate      `json:"failure_candidates"`
}

// BuildPipelineResult normalizes event envelopes and binds analytical output to
// the exact input evidence. It never creates or promotes persisted memories.
func BuildPipelineResult(tenantID, schemaVersion, normalizationVersion string, raw []RawEvent, analysis ports.AnalystResult, now time.Time) (PipelineResult, error) {
	if tenantID == "" || schemaVersion == "" || normalizationVersion == "" || len(raw) == 0 || now.IsZero() {
		return PipelineResult{}, ErrInvalidPipelineInput
	}
	batch := ports.AnalysisBatch{TenantID: tenantID, RunID: "normalization", Events: make([]ports.AnalysisEvent, 0, len(raw))}
	result := PipelineResult{Normalized: make([]NormalizedEvent, 0, len(raw))}
	ids := make(map[string]RawEvent, len(raw))
	for _, event := range raw {
		if event.ID == "" || event.TenantID != tenantID || event.OccurredAt.IsZero() || len(event.Payload) > 0 && !json.Valid(event.Payload) {
			return PipelineResult{}, ErrInvalidPipelineInput
		}
		if _, duplicate := ids[event.ID]; duplicate {
			return PipelineResult{}, ErrInvalidPipelineInput
		}
		ids[event.ID] = event
		payload := event.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		normalizedPayload, err := normalizePayload(payload, event.SourceType, event.MessageType)
		if err != nil {
			return PipelineResult{}, err
		}
		result.Normalized = append(result.Normalized, NormalizedEvent{ID: "normalized:" + event.ID, RawEventID: event.ID, SchemaVersion: schemaVersion, NormalizationVersion: normalizationVersion, TenantID: tenantID, SessionID: event.SessionID, Sequence: event.Sequence, ParentEventID: event.ParentEventID, Payload: normalizedPayload, CreatedAt: now.UTC()})
		batch.Events = append(batch.Events, ports.AnalysisEvent{EventID: event.ID, SessionID: event.SessionID, OccurredAt: event.OccurredAt, Payload: payload})
	}
	if err := ValidateResult(batch, analysis); err != nil {
		return PipelineResult{}, err
	}
	grouped := map[string][]string{}
	for _, event := range raw {
		if event.SessionID != "" {
			grouped[event.SessionID] = append(grouped[event.SessionID], event.ID)
		}
	}
	for session, eventIDs := range grouped {
		var fragments []string
		for _, event := range raw {
			if event.SessionID != session {
				continue
			}
			text := extractText(event.Payload)
			if text != "" {
				fragments = append(fragments, text)
			}
		}
		if len(fragments) > 0 {
			result.Summaries = append(result.Summaries, SessionSummary{TenantID: tenantID, SessionID: session, EventIDs: eventIDs, Text: strings.Join(fragments, " "), Confidence: .5})
		}
	}
	result.Candidates = append(result.Candidates, analysis.Candidates...)
	for _, failure := range analysis.Failures {
		session := ""
		for _, id := range failure.EventIDs {
			if ids[id].SessionID != "" {
				session = ids[id].SessionID
				break
			}
		}
		result.Failures = append(result.Failures, FailureCandidate{TenantID: tenantID, SessionID: session, EventIDs: append([]string(nil), failure.EventIDs...), Conclusion: failure.Conclusion, Attribution: failure.Attribution, Confidence: failure.Confidence})
	}
	return result, nil
}

func normalizePayload(payload json.RawMessage, source, messageType string) (json.RawMessage, error) {
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	normalized := map[string]any{"source_type": source, "message_type": messageType, "data": fields}
	return json.Marshal(normalized)
}
func extractText(payload json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(payload, &fields) != nil {
		return ""
	}
	for _, key := range []string{"text", "content", "message", "output"} {
		if text, ok := fields[key].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}
