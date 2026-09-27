package memory

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

type ScopeType string

const (
	ScopeUserGlobal ScopeType = "user-global"
	ScopeSession    ScopeType = "session"
)

type MemoryStatus string

const (
	StatusCandidate  MemoryStatus = "candidate"
	StatusActive     MemoryStatus = "active"
	StatusStable     MemoryStatus = "stable"
	StatusConflicted MemoryStatus = "conflicted"
	StatusExpired    MemoryStatus = "expired"
	StatusRejected   MemoryStatus = "rejected"
)

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilitySession Visibility = "session"
	VisibilityTenant  Visibility = "tenant"
)

type SessionStatus string

const (
	SessionActive      SessionStatus = "active"
	SessionClosing     SessionStatus = "closing"
	SessionClosed      SessionStatus = "closed"
	SessionInterrupted SessionStatus = "interrupted"
)

type EvidenceReliability string

const (
	ReliabilityHigh    EvidenceReliability = "high"
	ReliabilityMedium  EvidenceReliability = "medium"
	ReliabilityLow     EvidenceReliability = "low"
	ReliabilityUnknown EvidenceReliability = "unknown"
)

type Attribution string

const (
	AttributionDirect     Attribution = "direct"
	AttributionCorrelated Attribution = "correlated"
	AttributionInferred   Attribution = "inferred"
	AttributionUnknown    Attribution = "unknown"
)

type FeedbackType string

const (
	FeedbackAccepted  FeedbackType = "accepted"
	FeedbackHelpful   FeedbackType = "helpful"
	FeedbackHarmful   FeedbackType = "harmful"
	FeedbackCorrected FeedbackType = "corrected"
	FeedbackIgnored   FeedbackType = "ignored"
	FeedbackConflict  FeedbackType = "conflict"
	FeedbackExpired   FeedbackType = "expired"
)

type RelationType string

const (
	RelationSupports    RelationType = "supports"
	RelationContradicts RelationType = "contradicts"
	RelationSupersedes  RelationType = "supersedes"
	RelationRelated     RelationType = "related"
	RelationParentOf    RelationType = "parent-of"
)

type Applicability struct {
	Conditions []string          `json:"conditions,omitempty"`
	Exclusions []string          `json:"exclusions,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

func (a Applicability) Validate() error {
	conditions := make(map[string]struct{}, len(a.Conditions))
	for _, value := range a.Conditions {
		if value == "" {
			return errors.New("applicability condition cannot be empty")
		}
		conditions[value] = struct{}{}
	}
	for _, value := range a.Exclusions {
		if value == "" {
			return errors.New("applicability exclusion cannot be empty")
		}
		if _, conflicts := conditions[value]; conflicts {
			return errors.New("applicability condition and exclusion conflict")
		}
	}
	for key := range a.Attributes {
		if key == "" {
			return errors.New("applicability attribute key cannot be empty")
		}
	}
	return nil
}

// AppliesTo uses exact, explainable tags and key/value constraints; it does not
// infer applicability from semantic similarity.
func (a Applicability) AppliesTo(context map[string]string) bool {
	if a.Validate() != nil {
		return false
	}
	contains := func(value string) bool {
		if context[value] == "true" {
			return true
		}
		for _, candidate := range context {
			if candidate == value {
				return true
			}
		}
		return false
	}
	for _, exclusion := range a.Exclusions {
		if contains(exclusion) {
			return false
		}
	}
	for _, condition := range a.Conditions {
		if !contains(condition) {
			return false
		}
	}
	for key, expected := range a.Attributes {
		if context[key] != expected {
			return false
		}
	}
	return true
}

type Provenance struct {
	SourceEventIDs []string `json:"source_event_ids,omitempty"`
	RunID          string   `json:"run_id,omitempty"`
	Analyst        string   `json:"analyst,omitempty"`
	ModelVersion   string   `json:"model_version,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type Memory struct {
	ID               string          `json:"id"`
	IdempotencyKey   string          `json:"idempotency_key"`
	UserID           string          `json:"user_id"`
	SessionID        string          `json:"session_id,omitempty"`
	ScopeType        ScopeType       `json:"scope_type"`
	ScopeID          string          `json:"scope_id"`
	ParentID         string          `json:"parent_id,omitempty"`
	Type             string          `json:"type"`
	Status           MemoryStatus    `json:"status"`
	Visibility       Visibility      `json:"visibility"`
	Confidence       float64         `json:"confidence"`
	Applicability    Applicability   `json:"applicability"`
	Content          json.RawMessage `json:"content"`
	ContentText      string          `json:"content_text"`
	DefaultRetrieval bool            `json:"default_retrieval"`
	Version          int64           `json:"version"`
	Provenance       Provenance      `json:"provenance"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
}

func (m Memory) Validate() error {
	if m.ID == "" || m.IdempotencyKey == "" || m.UserID == "" || m.ScopeID == "" || m.Type == "" {
		return errors.New("memory identity and type are required")
	}
	if math.IsNaN(m.Confidence) || math.IsInf(m.Confidence, 0) || m.Confidence < 0 || m.Confidence > 1 {
		return errors.New("memory confidence must be finite and between zero and one")
	}
	if !isMemoryStatus(m.Status) {
		return errors.New("memory status is invalid")
	}
	if !json.Valid(m.Content) {
		return errors.New("memory content must be valid JSON")
	}
	if err := m.Applicability.Validate(); err != nil {
		return err
	}
	switch m.ScopeType {
	case ScopeUserGlobal:
		if m.SessionID != "" || m.ScopeID != m.UserID {
			return errors.New("user-global memory must be tenant-local to its user and have no session")
		}
	case ScopeSession:
		if m.SessionID == "" || m.ScopeID != m.SessionID {
			return errors.New("session memory must be bound to its session")
		}
	default:
		return errors.New("unsupported memory scope")
	}
	return nil
}

func isMemoryStatus(status MemoryStatus) bool {
	switch status {
	case StatusCandidate, StatusActive, StatusStable, StatusConflicted, StatusExpired, StatusRejected:
		return true
	default:
		return false
	}
}

type Evidence struct {
	ID                string              `json:"id"`
	MemoryID          string              `json:"memory_id"`
	RawEventID        string              `json:"raw_event_id"`
	NormalizedEventID string              `json:"normalized_event_id,omitempty"`
	SourceSessionID   string              `json:"source_session_id,omitempty"`
	Role              string              `json:"role"`
	Confidence        float64             `json:"confidence"`
	Reliability       EvidenceReliability `json:"reliability"`
	Attribution       Attribution         `json:"attribution"`
	Excerpt           string              `json:"excerpt,omitempty"`
	Metadata          json.RawMessage     `json:"metadata,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
}

type Relation struct {
	ID             string       `json:"id"`
	SourceMemoryID string       `json:"source_memory_id"`
	TargetMemoryID string       `json:"target_memory_id"`
	Type           RelationType `json:"type"`
	Confidence     float64      `json:"confidence"`
	CreatedAt      time.Time    `json:"created_at"`
}

type Feedback struct {
	ID        string       `json:"id"`
	MemoryID  string       `json:"memory_id"`
	UserID    string       `json:"user_id"`
	SessionID string       `json:"session_id,omitempty"`
	Type      FeedbackType `json:"type"`
	Reason    string       `json:"reason,omitempty"`
	RequestID string       `json:"request_id"`
	CreatedAt time.Time    `json:"created_at"`
}

type Session struct {
	ID             string          `json:"id"`
	UserID         string          `json:"user_id"`
	CreatedByAgent string          `json:"created_by_agent_id,omitempty"`
	Status         SessionStatus   `json:"status"`
	Title          string          `json:"title,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	EndedAt        *time.Time      `json:"ended_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type RetrievalMode string

const (
	RetrievalAuto    RetrievalMode = "auto"
	RetrievalFocus   RetrievalMode = "focus"
	RetrievalDiverge RetrievalMode = "diverge"
)

type ProjectedMemory struct {
	Memory          Memory   `json:"memory"`
	SelectionReason string   `json:"selection_reason"`
	EvidenceIDs     []string `json:"evidence_ids,omitempty"`
}

type ProjectionUsage struct {
	Candidates      int `json:"candidates"`
	Ranked          int `json:"ranked"`
	InjectedTokens  int `json:"injected_tokens"`
	InjectionBudget int `json:"injection_budget"`
}

type Projection struct {
	ID               string            `json:"id"`
	RequestID        string            `json:"request_id"`
	UserID           string            `json:"user_id"`
	SessionID        string            `json:"session_id,omitempty"`
	Mode             RetrievalMode     `json:"mode"`
	CurrentContext   string            `json:"current_context,omitempty"`
	Memories         []ProjectedMemory `json:"memories"`
	OpenLoops        []string          `json:"open_loops,omitempty"`
	Uncertainties    []string          `json:"uncertainties,omitempty"`
	SelectionReasons map[string]string `json:"selection_reasons,omitempty"`
	Usage            ProjectionUsage   `json:"usage"`
	DegradedMode     string            `json:"degraded_mode,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}
