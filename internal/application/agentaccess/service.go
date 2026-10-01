package agentaccess

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/observation"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// Projector, FeedbackRecorder and Consolidator are the application boundaries
// for the non-observe operations. They receive an already-bound tenant scope,
// never raw Envelope claims.
type Projector interface {
	Project(ctx context.Context, scoped Scoped, envelope dto.Envelope) (any, error)
}

type FeedbackRecorder interface {
	Feedback(ctx context.Context, scoped Scoped, envelope dto.Envelope) (any, error)
}

type Consolidator interface {
	Consolidate(ctx context.Context, scoped Scoped, envelope dto.Envelope) (any, error)
}

// Ingestor is the raw observation boundary; *observation.Gateway satisfies it.
// Each entry point fixes the event's source category so callers cannot spoof it
// through the payload.
type Ingestor interface {
	IngestMessage(ctx context.Context, principal observation.Principal, input observation.Input) (obsdomain.Event, bool, error)
	IngestTool(ctx context.Context, principal observation.Principal, input observation.Input) (obsdomain.Event, bool, error)
	IngestWorkflow(ctx context.Context, principal observation.Principal, input observation.Input) (obsdomain.Event, bool, error)
	IngestCode(ctx context.Context, principal observation.Principal, input observation.Input) (obsdomain.Event, bool, error)
	IngestGateway(ctx context.Context, principal observation.Principal, input observation.Input) (obsdomain.Event, bool, error)
}

type ServiceOptions struct {
	Gateway      Ingestor
	Sessions     ports.SessionBinder
	Projector    Projector
	Feedback     FeedbackRecorder
	Consolidator Consolidator
	Quarantine   ports.QuarantineRepository
	Projections  ProjectionLookup
	NewID        func() (string, error)
}

// ProjectionLookup resolves a projection reference to the identity of the
// caller that received it and the memories it exposed. It lets the service
// derive used-memory identity from server-side evidence instead of trusting a
// caller's own claim.
type ProjectionLookup interface {
	FindProjection(ctx context.Context, tenantID, projectionID string) (ports.ProjectionLookup, error)
}

type Service struct {
	binder       *Binder
	gateway      Ingestor
	sessions     ports.SessionBinder
	projector    Projector
	feedback     FeedbackRecorder
	consolidator Consolidator
	quarantine   ports.QuarantineRepository
	projections  ProjectionLookup
	newID        func() (string, error)
}

func NewService(options ServiceOptions) *Service {
	newID := options.NewID
	if newID == nil {
		newID = newUUID
	}
	return &Service{binder: NewBinder(), gateway: options.Gateway, sessions: options.Sessions, projector: options.Projector, feedback: options.Feedback, consolidator: options.Consolidator, quarantine: options.Quarantine, projections: options.Projections, newID: newID}
}

type ObservePayload struct {
	SourceEventID  string          `json:"source_event_id"`
	ConversationID string          `json:"conversation_id"`
	SourceType     string          `json:"source_type"`
	MessageType    string          `json:"message_type"`
	Sequence       *int64          `json:"sequence"`
	ParentEventID  string          `json:"parent_event_id"`
	Text           string          `json:"text"`
	Payload        json.RawMessage `json:"payload"`
	Trace          obsdomain.Trace `json:"trace"`
	OccurredAt     time.Time       `json:"occurred_at"`
}

// observeSource maps a declared source_type onto its observation entry point.
// An empty value keeps the historical user-message default so existing callers
// observe exactly what they observed before.
func observeSource(raw string) (obsdomain.SourceType, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", string(obsdomain.SourceUser):
		return obsdomain.SourceUser, nil
	case string(obsdomain.SourceAgent):
		return obsdomain.SourceAgent, nil
	case string(obsdomain.SourceTool):
		return obsdomain.SourceTool, nil
	case string(obsdomain.SourceWorkflow):
		return obsdomain.SourceWorkflow, nil
	case string(obsdomain.SourceGateway):
		return obsdomain.SourceGateway, nil
	default:
		return "", fmt.Errorf("%w: unsupported source_type %q", dto.ErrInvalidEnvelope, raw)
	}
}

type SessionPayload struct {
	ExternalID string `json:"external_id"`
	Title      string `json:"title"`
}

type ObserveResult struct {
	EventID   string `json:"event_id"`
	Created   bool   `json:"created"`
	TenantID  string `json:"tenant_id"`
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
}

type SessionResult struct {
	SessionID string `json:"session_id"`
	TenantID  string `json:"tenant_id"`
	RequestID string `json:"request_id"`
}

// QuarantineResult acknowledges an unbound envelope without processing it. The
// raw event is stored outside tenant schemas and never becomes memory.
type QuarantineResult struct {
	Quarantined  bool   `json:"quarantined"`
	QuarantineID string `json:"quarantine_id"`
	Reason       string `json:"reason"`
}

func (s *Service) quarantineObserve(ctx context.Context, envelope dto.Envelope, reason string) (any, error) {
	if s.quarantine == nil {
		return nil, fmt.Errorf("%w: %s", ErrForbidden, reason)
	}
	id, err := s.newID()
	if err != nil {
		return nil, err
	}
	payload := envelope.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := s.quarantine.QuarantineEvent(ctx, ports.QuarantinedEvent{
		ID:             id,
		ReceivedAt:     time.Now().UTC(),
		Reason:         reason,
		DeclaredTenant: envelope.Scope.TenantID,
		PrincipalType:  envelope.Principal.Type,
		PrincipalID:    envelope.Principal.ID,
		RequestID:      envelope.RequestID,
		Payload:        payload,
	}); err != nil {
		return nil, err
	}
	return QuarantineResult{Quarantined: true, QuarantineID: id, Reason: reason}, nil
}

func (s *Service) Observe(ctx context.Context, envelope dto.Envelope) (any, error) {
	identity, ok := IdentityFromContext(ctx)
	if !ok || identity.TenantID == "" {
		return s.quarantineObserve(ctx, envelope, "missing-tenant-credential")
	}
	scoped, identity, err := s.bind(ctx, envelope)
	if err != nil {
		if errors.Is(err, ErrCrossTenant) && s.quarantine != nil {
			return s.quarantineObserve(ctx, envelope, "cross-tenant-envelope")
		}
		return nil, err
	}
	if !scoped.HasCapability("observe") {
		return nil, fmt.Errorf("%w: observe capability not granted", ErrForbidden)
	}
	if s.gateway == nil {
		return nil, errors.New("observe handler is not configured")
	}
	var payload ObservePayload
	if err := decodePayload(envelope.Payload, &payload); err != nil {
		return nil, err
	}
	principal := PrincipalFor(identity, scoped)
	visibility := obsdomain.VisibilityPrivate
	if envelope.Privacy.Visibility == "tenant" {
		visibility = obsdomain.VisibilityTenant
	}
	source, err := observeSource(payload.SourceType)
	if err != nil {
		return nil, err
	}
	trace := payload.Trace
	s.resolveUsedMemoryIDs(ctx, scoped, &trace)
	input := observation.Input{
		SourceEventID:  payload.SourceEventID,
		IdempotencyKey: scoped.IdempotencyKey,
		SessionID:      scoped.SessionID,
		ConversationID: payload.ConversationID,
		MessageType:    strings.TrimSpace(payload.MessageType),
		Sequence:       payload.Sequence,
		ParentEventID:  strings.TrimSpace(payload.ParentEventID),
		Payload:        payload.Payload,
		PayloadText:    payload.Text,
		Trace:          trace,
		OccurredAt:     payload.OccurredAt,
		Visibility:     visibility,
		Reliability:    obsdomain.ReliabilityUnknown,
		RetentionClass: "standard",
	}
	var event obsdomain.Event
	var created bool
	switch source {
	case obsdomain.SourceTool:
		event, created, err = s.gateway.IngestTool(ctx, principal, input)
	case obsdomain.SourceWorkflow:
		event, created, err = s.gateway.IngestWorkflow(ctx, principal, input)
	case obsdomain.SourceAgent:
		event, created, err = s.gateway.IngestCode(ctx, principal, input)
	case obsdomain.SourceGateway:
		event, created, err = s.gateway.IngestGateway(ctx, principal, input)
	default:
		event, created, err = s.gateway.IngestMessage(ctx, principal, input)
	}
	if err != nil {
		return nil, err
	}
	return ObserveResult{EventID: event.ID, Created: created, TenantID: event.TenantID, SessionID: event.SessionID, RequestID: scoped.RequestID}, nil
}

// resolveUsedMemoryIDs fills the trace's used-memory identifiers from the
// projection the caller referenced, so the identity comes from server-side
// evidence rather than from a caller's claim.
//
// It deliberately stays silent on every failure mode: an unresolvable
// reference, a reference owned by another user, or a session mismatch all leave
// the identifiers empty and the observation is still accepted. The service
// never fabricates used-memory identity, and never reveals anything about a
// projection the caller does not own.
func (s *Service) resolveUsedMemoryIDs(ctx context.Context, scoped Scoped, trace *obsdomain.Trace) {
	if s.projections == nil || trace == nil || strings.TrimSpace(trace.ProjectionID) == "" || len(trace.UsedMemoryIDs) > 0 {
		return
	}
	lookup, err := s.projections.FindProjection(ctx, scoped.TenantID, strings.TrimSpace(trace.ProjectionID))
	if err != nil {
		return
	}
	if lookup.UserID == "" || lookup.UserID != scoped.UserID {
		return
	}
	if lookup.SessionID != "" && scoped.SessionID != "" && lookup.SessionID != scoped.SessionID {
		return
	}
	if len(lookup.SelectedMemoryIDs) == 0 {
		return
	}
	trace.UsedMemoryIDs = append([]string(nil), lookup.SelectedMemoryIDs...)
}

func (s *Service) Session(ctx context.Context, envelope dto.Envelope) (any, error) {
	scoped, _, err := s.bind(ctx, envelope)
	if err != nil {
		return nil, err
	}
	if s.sessions == nil {
		return nil, errors.New("session handler is not configured")
	}
	var payload SessionPayload
	if len(envelope.Payload) > 0 {
		if err := decodePayload(envelope.Payload, &payload); err != nil {
			return nil, err
		}
	}
	sessionID := scoped.SessionID
	if sessionID == "" {
		sessionID, err = newUUID()
		if err != nil {
			return nil, err
		}
	}
	boundID, err := s.sessions.BindSession(ctx, ports.SessionBinding{
		TenantID:   scoped.TenantID,
		SessionID:  sessionID,
		UserID:     scoped.UserID,
		AgentID:    scoped.AgentID,
		ExternalID: payload.ExternalID,
		Title:      payload.Title,
	})
	if err != nil {
		return nil, err
	}
	return SessionResult{SessionID: boundID, TenantID: scoped.TenantID, RequestID: scoped.RequestID}, nil
}

func (s *Service) Project(ctx context.Context, envelope dto.Envelope) (any, error) {
	scoped, _, err := s.bind(ctx, envelope)
	if err != nil {
		return nil, err
	}
	if !scoped.HasCapability("project") {
		return nil, fmt.Errorf("%w: project capability not granted", ErrForbidden)
	}
	if s.projector == nil {
		return nil, errors.New("project handler is not configured")
	}
	return s.projector.Project(ctx, scoped, envelope)
}

func (s *Service) Feedback(ctx context.Context, envelope dto.Envelope) (any, error) {
	scoped, _, err := s.bind(ctx, envelope)
	if err != nil {
		return nil, err
	}
	if !scoped.HasCapability("feedback") {
		return nil, fmt.Errorf("%w: feedback capability not granted", ErrForbidden)
	}
	if s.feedback == nil {
		return nil, errors.New("feedback handler is not configured")
	}
	return s.feedback.Feedback(ctx, scoped, envelope)
}

func (s *Service) Consolidate(ctx context.Context, envelope dto.Envelope) (any, error) {
	scoped, _, err := s.bind(ctx, envelope)
	if err != nil {
		return nil, err
	}
	if s.consolidator == nil {
		return nil, errors.New("consolidate handler is not configured")
	}
	return s.consolidator.Consolidate(ctx, scoped, envelope)
}

func (s *Service) bind(ctx context.Context, envelope dto.Envelope) (Scoped, Identity, error) {
	if err := envelope.Validate(); err != nil {
		return Scoped{}, Identity{}, err
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return Scoped{}, Identity{}, ErrNoIdentity
	}
	scoped, err := s.binder.Bind(identity, envelope)
	if err != nil {
		return Scoped{}, Identity{}, err
	}
	return scoped, identity, nil
}

// PrincipalFor maps a bound identity to a trusted observation principal. The
// observation gateway re-validates the level-specific binding flags so an
// Envelope can never downgrade or upgrade its own access level.
func PrincipalFor(identity Identity, scoped Scoped) observation.Principal {
	level := identity.AccessLevel
	sourceID := identity.SourceID
	if sourceID == "" {
		if identity.AgentID != "" {
			sourceID = identity.AgentID
		} else {
			sourceID = identity.UserID
		}
	}
	principal := observation.Principal{
		TenantID:    identity.TenantID,
		SourceID:    sourceID,
		AccessLevel: level,
	}
	switch identity.Source {
	case SourceDeployment:
		principal.DeploymentBound = true
		if principal.AccessLevel == "" {
			principal.AccessLevel = obsdomain.Level0
		}
	case SourceAgentContext:
		principal.AgentBound = true
		if principal.AccessLevel == "" {
			principal.AccessLevel = obsdomain.Level1
		}
	case SourceVerifiedUser:
		// A verified console session is a trusted binding for its own user:
		// its observations are admissible without an Agent context.
		principal.UserBound = true
		if principal.AccessLevel == "" {
			principal.AccessLevel = obsdomain.Level1
		}
	default:
		if principal.AccessLevel == "" {
			principal.AccessLevel = obsdomain.Level1
		}
	}
	return principal
}

func decodePayload(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: payload decode: %w", dto.ErrInvalidEnvelope, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing payload value", dto.ErrInvalidEnvelope)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
