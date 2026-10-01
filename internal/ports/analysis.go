package ports

import (
	"context"
	"encoding/json"
	"time"
)

// AnalysisBatch carries trusted tenant identity with the input; provider adapters
// must propagate this identity to cache keys, logs, and any tenant-specific state.
type AnalysisBatch struct {
	TenantID      string            `json:"tenant_id"`
	RunID         string            `json:"run_id"`
	Events        []AnalysisEvent   `json:"events"`
	PromptVersion string            `json:"prompt_version"`
	SchemaVersion string            `json:"schema_version"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type AnalysisEvent struct {
	EventID    string          `json:"event_id"`
	SessionID  string          `json:"session_id,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

type Classification struct {
	EventID    string  `json:"event_id"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
}

type FailureAssessment struct {
	EventIDs    []string `json:"event_ids"`
	Conclusion  string   `json:"conclusion"`
	Attribution string   `json:"attribution"`
	Confidence  float64  `json:"confidence"`
}

type CandidateMemory struct {
	CandidateID      string          `json:"candidate_id"`
	EvidenceEventIDs []string        `json:"evidence_event_ids"`
	ScopeType        string          `json:"scope_type"`
	ScopeID          string          `json:"scope_id"`
	Content          json.RawMessage `json:"content"`
	Confidence       float64         `json:"confidence"`
}

type ConflictAssessment struct {
	CandidateIDs []string `json:"candidate_ids"`
	Conflicted   bool     `json:"conflicted"`
	ReasonCode   string   `json:"reason_code"`
}

type AnalystResult struct {
	Classifications []Classification     `json:"classifications,omitempty"`
	Failures        []FailureAssessment  `json:"failures,omitempty"`
	Candidates      []CandidateMemory    `json:"candidates,omitempty"`
	Conflicts       []ConflictAssessment `json:"conflicts,omitempty"`
}

type MemoryAnalyst interface {
	Analyze(context.Context, AnalysisBatch) (AnalystResult, error)
}
type Classifier interface {
	Classify(context.Context, AnalysisBatch) ([]Classification, error)
}
type FailureAnalyzer interface {
	AnalyzeFailures(context.Context, AnalysisBatch) ([]FailureAssessment, error)
}
type Consolidator interface {
	Consolidate(context.Context, AnalysisBatch) ([]CandidateMemory, error)
}
type ConflictAnalyzer interface {
	AnalyzeConflicts(context.Context, AnalysisBatch, []CandidateMemory) ([]ConflictAssessment, error)
}
