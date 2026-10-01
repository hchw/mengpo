package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const CurrentVersion = "v1"

const (
	MaxRequestIDLength  = 128
	MaxHintItems        = 32
	MaxBudgetCandidates = 1000
	MaxBudgetRanking    = 1000
	MaxInjectionTokens  = 32768
	MaxRelationDepth    = 16
)

// ErrInvalidEnvelope rejects a request whose envelope or payload is malformed.
// It carries an API error code so the HTTP layer answers 400 instead of 500.
var ErrInvalidEnvelope = InvalidEnvelopeError{}

// ErrUnsupportedVersion rejects an envelope with an unknown version.
var ErrUnsupportedVersion = UnsupportedVersionError{}

// InvalidEnvelopeError is the typed form of ErrInvalidEnvelope.
type InvalidEnvelopeError struct{}

func (InvalidEnvelopeError) Error() string        { return "invalid API envelope" }
func (InvalidEnvelopeError) APIErrorCode() string { return "INVALID_ENVELOPE" }
func (InvalidEnvelopeError) Retryable() bool      { return false }

// UnsupportedVersionError is the typed form of ErrUnsupportedVersion.
type UnsupportedVersionError struct{}

func (UnsupportedVersionError) Error() string        { return "unsupported API version" }
func (UnsupportedVersionError) APIErrorCode() string { return "UNSUPPORTED_VERSION" }
func (UnsupportedVersionError) Retryable() bool      { return false }

// Envelope is the common v1 request wrapper. Scope values are client claims,
// never authorization evidence; handlers must resolve them against an
// authenticated principal and tenant membership before accessing data.
type Envelope struct {
	Version        string          `json:"version"`
	RequestID      string          `json:"request_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Principal      Principal       `json:"principal"`
	Scope          Scope           `json:"scope"`
	MemoryHint     *MemoryHint     `json:"memory_hint,omitempty"`
	Budget         Budget          `json:"budget"`
	Privacy        Privacy         `json:"privacy"`
	Payload        json.RawMessage `json:"payload"`
}

type Principal struct {
	Type string `json:"type"` // user | agent
	ID   string `json:"id"`
}

type Scope struct {
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id"`
	Type      string `json:"type"` // user-global | session
	SessionID string `json:"session_id,omitempty"`
}

type MemoryHint struct {
	Mode            string   `json:"mode,omitempty"` // auto | focus | diverge
	Topics          []string `json:"topics,omitempty"`
	MemoryIDs       []string `json:"memory_ids,omitempty"`
	AllowCandidates *bool    `json:"allow_candidates,omitempty"` // policy may still deny
}

type Budget struct {
	Candidates      int `json:"candidates,omitempty"`
	Ranking         int `json:"ranking,omitempty"`
	InjectionTokens int `json:"injection_tokens,omitempty"`
	RelationDepth   int `json:"relation_depth,omitempty"`
}

type Privacy struct {
	Visibility         string   `json:"visibility"` // private | tenant
	AllowExternalLLM   bool     `json:"allow_external_llm,omitempty"`
	SensitiveFields    []string `json:"sensitive_fields,omitempty"`
	IncludeRawEvidence bool     `json:"include_raw_evidence,omitempty"`
}

func (e Envelope) Validate() error {
	if e.Version != CurrentVersion {
		return fmt.Errorf("%w: %q", ErrUnsupportedVersion, e.Version)
	}
	if !validToken(e.RequestID, MaxRequestIDLength) || !validToken(e.IdempotencyKey, MaxRequestIDLength) {
		return fmt.Errorf("%w: request_id and idempotency_key are required bounded tokens", ErrInvalidEnvelope)
	}
	if (e.Principal.Type != "user" && e.Principal.Type != "agent") || !validToken(e.Principal.ID, MaxRequestIDLength) {
		return fmt.Errorf("%w: principal type/id is invalid", ErrInvalidEnvelope)
	}
	if !validToken(e.Scope.TenantID, MaxRequestIDLength) || !validToken(e.Scope.UserID, MaxRequestIDLength) {
		return fmt.Errorf("%w: tenant and user scope are required", ErrInvalidEnvelope)
	}
	switch e.Scope.Type {
	case "user-global":
		if e.Scope.SessionID != "" {
			return fmt.Errorf("%w: user-global scope cannot declare session_id", ErrInvalidEnvelope)
		}
	case "session":
		if !validToken(e.Scope.SessionID, MaxRequestIDLength) {
			return fmt.Errorf("%w: session scope requires session_id", ErrInvalidEnvelope)
		}
	default:
		return fmt.Errorf("%w: unsupported memory scope %q", ErrInvalidEnvelope, e.Scope.Type)
	}
	if e.MemoryHint != nil {
		if e.MemoryHint.Mode != "" && e.MemoryHint.Mode != "auto" && e.MemoryHint.Mode != "focus" && e.MemoryHint.Mode != "diverge" {
			return fmt.Errorf("%w: unsupported memory hint mode %q", ErrInvalidEnvelope, e.MemoryHint.Mode)
		}
		if len(e.MemoryHint.Topics) > MaxHintItems || len(e.MemoryHint.MemoryIDs) > MaxHintItems {
			return fmt.Errorf("%w: memory hint exceeds item limit", ErrInvalidEnvelope)
		}
		for _, topic := range e.MemoryHint.Topics {
			if strings.TrimSpace(topic) == "" || len(topic) > 256 {
				return fmt.Errorf("%w: invalid memory hint topic", ErrInvalidEnvelope)
			}
		}
	}
	if e.Budget.Candidates < 0 || e.Budget.Candidates > MaxBudgetCandidates || e.Budget.Ranking < 0 || e.Budget.Ranking > MaxBudgetRanking || e.Budget.InjectionTokens < 0 || e.Budget.InjectionTokens > MaxInjectionTokens || e.Budget.RelationDepth < 0 || e.Budget.RelationDepth > MaxRelationDepth {
		return fmt.Errorf("%w: budget is outside supported bounds", ErrInvalidEnvelope)
	}
	if e.Privacy.Visibility != "private" && e.Privacy.Visibility != "tenant" {
		return fmt.Errorf("%w: unsupported visibility %q", ErrInvalidEnvelope, e.Privacy.Visibility)
	}
	if len(e.Privacy.SensitiveFields) > MaxHintItems {
		return fmt.Errorf("%w: too many sensitive fields", ErrInvalidEnvelope)
	}
	if len(e.Payload) == 0 || !json.Valid(e.Payload) || bytes.Equal(bytes.TrimSpace(e.Payload), []byte("null")) {
		return fmt.Errorf("%w: payload must be a JSON value", ErrInvalidEnvelope)
	}
	return nil
}

// DecodeEnvelope strictly decodes one JSON object, rejects unknown fields and
// trailing values, and validates the resulting v1 request contract.
func DecodeEnvelope(reader io.Reader) (Envelope, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: decode: %w", ErrInvalidEnvelope, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Envelope{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidEnvelope)
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func validToken(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value
}
