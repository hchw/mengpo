package observation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidPrincipal = errors.New("trusted observation principal is invalid")

// Principal must be constructed by the authentication/deployment binding layer,
// not from fields in the event payload.
type Principal struct {
	TenantID        string
	SourceID        string
	SourceType      observation.SourceType
	AccessLevel     observation.AccessLevel
	DeploymentBound bool
	AgentBound      bool
}

type Input struct {
	SourceEventID  string
	IdempotencyKey string
	SessionID      string
	ConversationID string
	MessageType    string
	Payload        json.RawMessage
	PayloadText    string
	Sequence       *int64
	ParentEventID  string
	OccurredAt     time.Time
	Visibility     observation.Visibility
	Reliability    observation.Reliability
	RetentionClass string
	Trace          observation.Trace
}

type Gateway struct {
	repository ports.ObservationRepository
	notifier   ports.PubSub
	clock      func() time.Time
	newID      func() (string, error)
}

func NewGateway(repository ports.ObservationRepository, notifier ...ports.PubSub) *Gateway {
	gateway := &Gateway{repository: repository, clock: time.Now, newID: uuid}
	if len(notifier) > 0 {
		gateway.notifier = notifier[0]
	}
	return gateway
}

// Ingest persists a raw observation. Duplicate idempotency keys return the
// previously stored event; no memory or analysis result is written here.
func (g *Gateway) Ingest(ctx context.Context, principal Principal, input Input) (observation.Event, bool, error) {
	if g == nil || g.repository == nil || strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.SourceID) == "" {
		return observation.Event{}, false, ErrInvalidPrincipal
	}
	if !validSource(principal.SourceType) {
		return observation.Event{}, false, ErrInvalidPrincipal
	}
	if principal.AccessLevel == "" {
		principal.AccessLevel = observation.Level0
	}
	if principal.AccessLevel == observation.Level0 {
		if !principal.DeploymentBound || (input.SessionID == "" && input.ConversationID == "") {
			return observation.Event{}, false, ErrInvalidPrincipal
		}
	} else if (principal.AccessLevel != observation.Level1 && principal.AccessLevel != observation.Level2) || !principal.AgentBound {
		return observation.Event{}, false, ErrInvalidPrincipal
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = g.clock().UTC()
	}
	id, err := g.newID()
	if err != nil {
		return observation.Event{}, false, fmt.Errorf("create observation id: %w", err)
	}
	event := observation.Event{
		ID: id, SourceEventID: input.SourceEventID, IdempotencyKey: input.IdempotencyKey,
		TenantID: principal.TenantID, SessionID: input.SessionID, ConversationID: input.ConversationID,
		SourceType: principal.SourceType, SourceID: principal.SourceID, MessageType: input.MessageType,
		Payload: input.Payload, PayloadText: input.PayloadText, Sequence: input.Sequence,
		ParentEventID: input.ParentEventID, OccurredAt: input.OccurredAt.UTC(),
		Visibility: input.Visibility, Reliability: input.Reliability, RetentionClass: input.RetentionClass,
		AccessLevel: principal.AccessLevel, Trace: input.Trace,
	}
	event.Attribution = observation.AssessAttribution(event.SessionID, event.ConversationID, event.ParentEventID, event.Trace)
	if err := event.Validate(); err != nil {
		return observation.Event{}, false, err
	}
	stored, created, err := g.repository.StoreObservation(ctx, event)
	if err != nil {
		return observation.Event{}, false, err
	}
	if created && g.notifier != nil {
		// A missed wake-up is harmless: PostgreSQL workers independently poll the durable outbox.
		_ = g.notifier.Publish(ctx, ports.JobNotification{TenantID: stored.TenantID, JobID: stored.ID})
	}
	return stored, created, nil
}

// These source adapters share the same validation and persistence path while
// fixing the event category, avoiding caller-controlled source_type values.
func (g *Gateway) IngestMessage(ctx context.Context, p Principal, in Input) (observation.Event, bool, error) {
	p.SourceType = observation.SourceUser
	in.MessageType = "message"
	return g.Ingest(ctx, p, in)
}
func (g *Gateway) IngestTool(ctx context.Context, p Principal, in Input) (observation.Event, bool, error) {
	p.SourceType = observation.SourceTool
	if in.MessageType == "" {
		in.MessageType = "tool.result"
	}
	return g.Ingest(ctx, p, in)
}
func (g *Gateway) IngestWorkflow(ctx context.Context, p Principal, in Input) (observation.Event, bool, error) {
	p.SourceType = observation.SourceWorkflow
	if in.MessageType == "" {
		in.MessageType = "workflow.event"
	}
	return g.Ingest(ctx, p, in)
}
func (g *Gateway) IngestCode(ctx context.Context, p Principal, in Input) (observation.Event, bool, error) {
	p.SourceType = observation.SourceAgent
	if in.MessageType == "" {
		in.MessageType = "code.event"
	}
	return g.Ingest(ctx, p, in)
}
func (g *Gateway) IngestFeedback(ctx context.Context, p Principal, in Input) (observation.Event, bool, error) {
	p.SourceType = observation.SourceUser
	in.MessageType = "feedback"
	return g.Ingest(ctx, p, in)
}

func validSource(source observation.SourceType) bool {
	switch source {
	case observation.SourceUser, observation.SourceAgent, observation.SourceTool, observation.SourceWorkflow, observation.SourceGateway:
		return true
	default:
		return false
	}
}

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexID := hex.EncodeToString(b[:])
	return hexID[:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:], nil
}
