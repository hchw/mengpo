package observation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

type memoryRepository struct{ events []observation.Event }
type notificationRecorder struct {
	notifications []ports.JobNotification
	err           error
}

func (n *notificationRecorder) Publish(_ context.Context, notification ports.JobNotification) error {
	n.notifications = append(n.notifications, notification)
	return n.err
}
func (n *notificationRecorder) Subscribe(context.Context) (<-chan ports.JobNotification, error) {
	return nil, nil
}

func (r *memoryRepository) StoreObservation(_ context.Context, event observation.Event) (observation.Event, bool, error) {
	r.events = append(r.events, event)
	return event, true, nil
}

func TestGatewayAdaptsAllObservationSources(t *testing.T) {
	repo := &memoryRepository{}
	gateway := NewGateway(repo)
	gateway.newID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	input := Input{IdempotencyKey: "key", SessionID: "session", Payload: json.RawMessage(`{"ok":true}`),
		OccurredAt: time.Now(), Visibility: observation.VisibilitySession,
		Reliability: observation.ReliabilityHigh, RetentionClass: "standard"}
	cases := []struct {
		name       string
		call       func(context.Context, Principal, Input) (observation.Event, bool, error)
		wantSource observation.SourceType
		wantType   string
	}{
		{"message", gateway.IngestMessage, observation.SourceUser, "message"},
		{"tool", gateway.IngestTool, observation.SourceTool, "tool.result"},
		{"workflow", gateway.IngestWorkflow, observation.SourceWorkflow, "workflow.event"},
		{"code", gateway.IngestCode, observation.SourceAgent, "code.event"},
		{"feedback", gateway.IngestFeedback, observation.SourceUser, "feedback"},
		{"gateway", gateway.IngestGateway, observation.SourceGateway, "gateway.event"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Principal{TenantID: "trusted-tenant", SourceID: "trusted-source", AccessLevel: observation.Level1, AgentBound: true}
			event, created, err := tc.call(context.Background(), p, input)
			if err != nil {
				t.Fatal(err)
			}
			if !created || event.SourceType != tc.wantSource || event.MessageType != tc.wantType || event.TenantID != p.TenantID {
				t.Fatalf("unexpected adapted event: %#v created=%v", event, created)
			}
		})
	}
	if len(repo.events) != len(cases) {
		t.Fatalf("persisted %d events, want %d", len(repo.events), len(cases))
	}
}

func TestGatewayPreservesRawEventWhenNotificationFails(t *testing.T) {
	repo := &memoryRepository{}
	notifier := &notificationRecorder{err: errors.New("NATS unavailable")}
	gateway := NewGateway(repo, notifier)
	gateway.newID = func() (string, error) { return "event-id", nil }
	input := Input{IdempotencyKey: "key", SessionID: "session", MessageType: "message", Payload: json.RawMessage(`{}`), OccurredAt: time.Now(), Visibility: observation.VisibilitySession, Reliability: observation.ReliabilityUnknown, RetentionClass: "standard"}
	event, created, err := gateway.Ingest(context.Background(), Principal{TenantID: "tenant", SourceID: "user", SourceType: observation.SourceUser, AccessLevel: observation.Level0, DeploymentBound: true}, input)
	if err != nil || !created || event.ID != "event-id" {
		t.Fatalf("Ingest()=%#v,%v,%v", event, created, err)
	}
	if len(repo.events) != 1 {
		t.Fatalf("durable repository received %d observations", len(repo.events))
	}
}

func TestGatewayEnforcesProgressiveAccessLevels(t *testing.T) {
	gateway := NewGateway(&memoryRepository{})
	input := Input{IdempotencyKey: "key", MessageType: "tool.result", Payload: json.RawMessage(`{}`), OccurredAt: time.Now(), Visibility: observation.VisibilitySession, Reliability: observation.ReliabilityUnknown, RetentionClass: "standard", SessionID: "session"}
	if _, _, err := gateway.Ingest(context.Background(), Principal{TenantID: "t", SourceID: "deployment", SourceType: observation.SourceGateway, AccessLevel: observation.Level0}, input); err != ErrInvalidPrincipal {
		t.Fatalf("unbound Level 0 error=%v", err)
	}
	if _, _, err := gateway.Ingest(context.Background(), Principal{TenantID: "t", SourceID: "deployment", SourceType: observation.SourceGateway, AccessLevel: observation.Level0, DeploymentBound: true}, input); err != nil {
		t.Fatalf("bound Level 0: %v", err)
	}
	if _, _, err := gateway.Ingest(context.Background(), Principal{TenantID: "t", SourceID: "agent", SourceType: observation.SourceAgent, AccessLevel: observation.Level1, AgentBound: true}, input); err != nil {
		t.Fatalf("Level 1: %v", err)
	}
	deep := input
	deep.Trace = observation.Trace{TaskID: "task", AttemptID: "attempt", ProjectionID: "projection", UsedMemoryIDs: []string{"memory"}, ToolResultID: "result", OutcomeID: "outcome"}
	event, _, err := gateway.Ingest(context.Background(), Principal{TenantID: "t", SourceID: "agent", SourceType: observation.SourceAgent, AccessLevel: observation.Level2, AgentBound: true}, deep)
	if err != nil {
		t.Fatalf("Level 2: %v", err)
	}
	if event.Attribution.Level != observation.AttributionDirect {
		t.Fatalf("Level 2 attribution=%s, want direct", event.Attribution.Level)
	}
}

// TestGatewayAdmitsVerifiedUserSession proves a Level 1 principal bound to a
// verified end-user session is admissible (the console /api/v1/observe path),
// while an equivalent principal with no binding is still rejected.
func TestGatewayAdmitsVerifiedUserSession(t *testing.T) {
	gateway := NewGateway(&memoryRepository{})
	input := Input{
		IdempotencyKey: "key",
		MessageType:    "message",
		Payload:        json.RawMessage(`{}`),
		OccurredAt:     time.Now(),
		Visibility:     observation.VisibilitySession,
		Reliability:    observation.ReliabilityUnknown,
		RetentionClass: "standard",
		SessionID:      "session",
	}
	user := Principal{
		TenantID:    "t",
		SourceID:    "user",
		SourceType:  observation.SourceUser,
		AccessLevel: observation.Level1,
		UserBound:   true,
	}
	if _, _, err := gateway.IngestMessage(context.Background(), user, input); err != nil {
		t.Fatalf("user-bound Level 1: %v", err)
	}
	unbound := user
	unbound.UserBound = false
	if _, _, err := gateway.IngestMessage(context.Background(), unbound, input); err != ErrInvalidPrincipal {
		t.Fatalf("unbound Level 1 error = %v, want %v", err, ErrInvalidPrincipal)
	}
	// The sentinel carries an API code so the HTTP layer answers 400.
	coded, ok := any(ErrInvalidPrincipal).(interface{ APIErrorCode() string })
	if !ok || coded.APIErrorCode() != "INVALID_PRINCIPAL" {
		t.Fatalf("ErrInvalidPrincipal is not a coded error: %T", ErrInvalidPrincipal)
	}
}

func TestGatewayRequiresTrustedTenantAndSource(t *testing.T) {
	gateway := NewGateway(&memoryRepository{})
	_, _, err := gateway.IngestMessage(context.Background(), Principal{SourceID: "source"}, Input{})
	if err != ErrInvalidPrincipal {
		t.Fatalf("error = %v, want %v", err, ErrInvalidPrincipal)
	}
}

// TestGatewayKeepsDeclaredMessageType proves a caller-declared event type is
// preserved (it used to be silently overwritten with "message"), that the
// default still applies when the caller declares none, and that the declared
// type is what finally drives rule-based detection at the ingress.
func TestGatewayKeepsDeclaredMessageType(t *testing.T) {
	repo := &memoryRepository{}
	gateway := NewGateway(repo)
	gateway.newID = func() (string, error) { return "00000000-0000-4000-8000-000000000002", nil }
	principal := Principal{TenantID: "trusted-tenant", SourceID: "trusted-source", AccessLevel: observation.Level1, AgentBound: true}
	base := Input{IdempotencyKey: "key-1", SessionID: "session", Payload: json.RawMessage(`{}`), OccurredAt: time.Now(), Visibility: observation.VisibilitySession, Reliability: observation.ReliabilityHigh, RetentionClass: "standard"}

	declared := base
	declared.IdempotencyKey = "key-declared"
	declared.MessageType = "tool.failure"
	event, _, err := gateway.IngestTool(context.Background(), principal, declared)
	if err != nil {
		t.Fatal(err)
	}
	if event.MessageType != "tool.failure" {
		t.Fatalf("declared message type = %q, want tool.failure", event.MessageType)
	}
	if event.SourceType != observation.SourceTool {
		t.Fatalf("source type = %q, want tool", event.SourceType)
	}
	if task, required := observation.AnalysisTask(event); !required || task != observation.TaskFailureAnalysis {
		t.Fatalf("AnalysisTask() = (%q, %v), want failure analysis", task, required)
	}

	omitted := base
	omitted.IdempotencyKey = "key-omitted"
	fallback, _, err := gateway.IngestMessage(context.Background(), principal, omitted)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.MessageType != "message" {
		t.Fatalf("default message type = %q, want message", fallback.MessageType)
	}
}

// TestGatewayDeclaredEventTypeIsReachableOverIngress records the behaviour change
// this change introduces: a user-source event that expresses its meaning only
// through the declared event type now reaches rule-based detection. Before, the
// type was overwritten with "message" and such an event could never be detected.
func TestGatewayDeclaredEventTypeIsReachableOverIngress(t *testing.T) {
	repo := &memoryRepository{}
	gateway := NewGateway(repo)
	gateway.newID = func() (string, error) { return "00000000-0000-4000-8000-000000000003", nil }
	principal := Principal{TenantID: "trusted-tenant", SourceID: "trusted-source", AccessLevel: observation.Level1, UserBound: true}

	correction := Input{
		IdempotencyKey: "key-correction", SessionID: "session", MessageType: "user_correction",
		Payload: json.RawMessage(`{}`), OccurredAt: time.Now(),
		Visibility: observation.VisibilityPrivate, Reliability: observation.ReliabilityUnknown, RetentionClass: "standard",
	}
	event, _, err := gateway.IngestMessage(context.Background(), principal, correction)
	if err != nil {
		t.Fatal(err)
	}
	if event.MessageType != "user_correction" {
		t.Fatalf("message type = %q, want the declared user_correction", event.MessageType)
	}
	if task, required := observation.AnalysisTask(event); !required || task != observation.TaskConsolidation {
		t.Fatalf("AnalysisTask() = (%q, %v), want consolidation from the declared type alone", task, required)
	}

	// An ordinary message with no declared type still stays out of the model path.
	ordinary := correction
	ordinary.IdempotencyKey = "key-ordinary"
	ordinary.MessageType = ""
	plain, _, err := gateway.IngestMessage(context.Background(), principal, ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if task, required := observation.AnalysisTask(plain); required {
		t.Fatalf("ordinary message must not require analysis, got %q", task)
	}
}
