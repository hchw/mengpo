package observation

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type SourceType string

const (
	SourceUser     SourceType = "user"
	SourceAgent    SourceType = "agent"
	SourceTool     SourceType = "tool"
	SourceWorkflow SourceType = "workflow"
	SourceGateway  SourceType = "gateway"
)

type Reliability string

const (
	ReliabilityHigh    Reliability = "high"
	ReliabilityMedium  Reliability = "medium"
	ReliabilityLow     Reliability = "low"
	ReliabilityUnknown Reliability = "unknown"
)

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilitySession Visibility = "session"
	VisibilityTenant  Visibility = "tenant"
)

type Event struct {
	ID             string          `json:"id"`
	SourceEventID  string          `json:"source_event_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	TenantID       string          `json:"tenant_id"`
	SessionID      string          `json:"session_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	SourceType     SourceType      `json:"source_type"`
	SourceID       string          `json:"source_id"`
	MessageType    string          `json:"message_type"`
	Payload        json.RawMessage `json:"payload"`
	PayloadText    string          `json:"payload_text,omitempty"`
	Sequence       *int64          `json:"sequence,omitempty"`
	ParentEventID  string          `json:"parent_event_id,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Visibility     Visibility      `json:"visibility"`
	Reliability    Reliability     `json:"reliability"`
	RetentionClass string          `json:"retention_class"`
	AccessLevel    AccessLevel     `json:"access_level,omitempty"`
	Trace          Trace           `json:"trace,omitempty"`
	Attribution    Attribution     `json:"attribution"`
}

var ErrInvalidEvent = errors.New("invalid observation event")

func (e Event) Validate() error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.TenantID) == "" ||
		strings.TrimSpace(e.IdempotencyKey) == "" || strings.TrimSpace(e.SourceID) == "" ||
		strings.TrimSpace(e.MessageType) == "" || strings.TrimSpace(e.RetentionClass) == "" || e.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	switch e.SourceType {
	case SourceUser, SourceAgent, SourceTool, SourceWorkflow, SourceGateway:
	default:
		return ErrInvalidEvent
	}
	switch e.Visibility {
	case VisibilityPrivate, VisibilitySession, VisibilityTenant:
	default:
		return ErrInvalidEvent
	}
	switch e.Reliability {
	case ReliabilityHigh, ReliabilityMedium, ReliabilityLow, ReliabilityUnknown:
	default:
		return ErrInvalidEvent
	}
	if e.AccessLevel != "" && e.AccessLevel != Level0 && e.AccessLevel != Level1 && e.AccessLevel != Level2 {
		return ErrInvalidEvent
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return ErrInvalidEvent
	}
	if e.Sequence != nil && *e.Sequence < 0 {
		return ErrInvalidEvent
	}
	return nil
}
