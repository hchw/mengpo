package agentaccess

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/observation"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

func envelope(scope dto.Scope, principal dto.Principal, payload string) dto.Envelope {
	return dto.Envelope{
		Version:        dto.CurrentVersion,
		RequestID:      "req-1",
		IdempotencyKey: "idem-1",
		Principal:      principal,
		Scope:          scope,
		Privacy:        dto.Privacy{Visibility: "private"},
		Payload:        json.RawMessage(payload),
	}
}

func agentIdentity() Identity {
	return Identity{TenantID: "tenant-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a", ScopeType: "session", Capabilities: []string{"observe", "project", "feedback"}, Source: SourceAgentContext, AccessLevel: obsdomain.Level1}
}

func TestBinderAcceptsMatchingTenantAndRejectsCrossTenant(t *testing.T) {
	binder := NewBinder()
	scoped, err := binder.Bind(agentIdentity(), envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{}`))
	if err != nil || scoped.TenantID != "tenant-a" || scoped.AgentID != "agent-a" {
		t.Fatalf("valid bind failed: %#v err=%v", scoped, err)
	}
	cases := []struct {
		name string
		env  dto.Envelope
		want error
	}{
		{"cross tenant", envelope(dto.Scope{TenantID: "tenant-b", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{}`), ErrCrossTenant},
		{"cross user", envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-b", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{}`), ErrForbidden},
		{"wrong agent", envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-z"}, `{}`), ErrForbidden},
		{"wrong session", envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-z"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{}`), ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := binder.Bind(agentIdentity(), tc.env); !errors.Is(err, tc.want) {
				t.Fatalf("Bind() err=%v want %v", err, tc.want)
			}
		})
	}
	if _, err := binder.Bind(Identity{}, envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "s"}, dto.Principal{Type: "user", ID: "user-a"}, `{}`)); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("empty identity accepted: %v", err)
	}
}

type fakeIngestor struct {
	principal observation.Principal
	input     observation.Input
	event     obsdomain.Event
	created   bool
	err       error
}

func (f *fakeIngestor) IngestMessage(_ context.Context, p observation.Principal, in observation.Input) (obsdomain.Event, bool, error) {
	f.principal = p
	f.input = in
	return f.event, f.created, f.err
}

type fakeBinder struct {
	binding ports.SessionBinding
	result  string
	err     error
}

func (f *fakeBinder) BindSession(_ context.Context, binding ports.SessionBinding) (string, error) {
	f.binding = binding
	return f.result, f.err
}

func TestObserveDelegatesTenantBoundIdentityAndPayload(t *testing.T) {
	ingestor := &fakeIngestor{event: obsdomain.Event{ID: "event-1", TenantID: "tenant-a", SessionID: "session-a"}, created: true}
	service := NewService(ServiceOptions{Gateway: ingestor})
	ctx := WithIdentity(context.Background(), agentIdentity())
	payload := `{"source_event_id":"src-1","text":"hello","payload":{"kind":"message"}}`
	result, err := service.Observe(ctx, envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, payload))
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(ObserveResult)
	if !ok || response.EventID != "event-1" || !response.Created {
		t.Fatalf("result=%#v", result)
	}
	if ingestor.principal.TenantID != "tenant-a" || ingestor.principal.SourceID != "agent-a" || !ingestor.principal.AgentBound {
		t.Fatalf("principal=%#v", ingestor.principal)
	}
	if ingestor.input.SessionID != "session-a" || ingestor.input.SourceEventID != "src-1" || ingestor.input.PayloadText != "hello" {
		t.Fatalf("input=%#v", ingestor.input)
	}
}

func TestObserveRejectsCrossTenantBeforeIngest(t *testing.T) {
	ingestor := &fakeIngestor{}
	service := NewService(ServiceOptions{Gateway: ingestor})
	ctx := WithIdentity(context.Background(), agentIdentity())
	_, err := service.Observe(ctx, envelope(dto.Scope{TenantID: "tenant-evil", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"text":"x"}`))
	if !errors.Is(err, ErrCrossTenant) {
		t.Fatalf("err=%v want cross-tenant", err)
	}
	if ingestor.principal.TenantID != "" {
		t.Fatalf("cross-tenant envelope reached ingest: %#v", ingestor.principal)
	}
}

func TestObserveRejectsMissingCapability(t *testing.T) {
	service := NewService(ServiceOptions{Gateway: &fakeIngestor{}})
	identity := agentIdentity()
	identity.Capabilities = []string{"project"}
	ctx := WithIdentity(context.Background(), identity)
	if _, err := service.Observe(ctx, envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"text":"x"}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v want forbidden", err)
	}
}

func TestSessionBindsExternalIdentityIdempotently(t *testing.T) {
	binder := &fakeBinder{result: "session-a"}
	service := NewService(ServiceOptions{Sessions: binder})
	ctx := WithIdentity(context.Background(), agentIdentity())
	result, err := service.Session(ctx, envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"external_id":"ext-1","title":"chat"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(SessionResult)
	if !ok || response.SessionID != "session-a" {
		t.Fatalf("result=%#v", result)
	}
	if binder.binding.TenantID != "tenant-a" || binder.binding.UserID != "user-a" || binder.binding.AgentID != "agent-a" || binder.binding.ExternalID != "ext-1" {
		t.Fatalf("binding=%#v", binder.binding)
	}
}

func TestProjectAndFeedbackEnforceBoundScopeAndCapability(t *testing.T) {
	projector := &recordingProjector{}
	feedback := &recordingFeedback{}
	service := NewService(ServiceOptions{Projector: projector, Feedback: feedback})
	ctx := WithIdentity(context.Background(), agentIdentity())
	env := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"query":"q"}`)
	if _, err := service.Project(ctx, env); err != nil || projector.scoped.TenantID != "tenant-a" {
		t.Fatalf("project err=%v scoped=%#v", err, projector.scoped)
	}
	if _, err := service.Feedback(ctx, env); err != nil || feedback.scoped.UserID != "user-a" {
		t.Fatalf("feedback err=%v scoped=%#v", err, feedback.scoped)
	}
}

type recordingProjector struct{ scoped Scoped }

func (r *recordingProjector) Project(_ context.Context, scoped Scoped, _ dto.Envelope) (any, error) {
	r.scoped = scoped
	return map[string]string{"ok": "project"}, nil
}

type recordingFeedback struct{ scoped Scoped }

func (r *recordingFeedback) Feedback(_ context.Context, scoped Scoped, _ dto.Envelope) (any, error) {
	r.scoped = scoped
	return map[string]string{"ok": "feedback"}, nil
}

func TestServiceRequiresTrustedIdentity(t *testing.T) {
	service := NewService(ServiceOptions{Gateway: &fakeIngestor{}})
	if _, err := service.Observe(context.Background(), envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "s"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"text":"x"}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v want forbidden without identity and no quarantine sink", err)
	}
	_ = time.Now
}

type fakeQuarantine struct {
	events []ports.QuarantinedEvent
	err    error
}

func (f *fakeQuarantine) QuarantineEvent(_ context.Context, event ports.QuarantinedEvent) error {
	f.events = append(f.events, event)
	return f.err
}

func TestObserveQuarantinesEnvelopeWithoutTenantCredential(t *testing.T) {
	ingestor := &fakeIngestor{}
	quarantine := &fakeQuarantine{}
	service := NewService(ServiceOptions{Gateway: ingestor, Quarantine: quarantine})
	env := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"text":"poison"}`)
	result, err := service.Observe(context.Background(), env)
	if err != nil {
		t.Fatalf("observe without identity: %v", err)
	}
	response, ok := result.(QuarantineResult)
	if !ok || !response.Quarantined || response.Reason != "missing-tenant-credential" {
		t.Fatalf("result=%#v", result)
	}
	if len(quarantine.events) != 1 || quarantine.events[0].DeclaredTenant != "tenant-a" {
		t.Fatalf("quarantined events=%#v", quarantine.events)
	}
	if ingestor.principal.TenantID != "" {
		t.Fatalf("quarantined event reached observation ingest: %#v", ingestor.principal)
	}
}

func TestObserveQuarantinesCrossTenantEnvelope(t *testing.T) {
	quarantine := &fakeQuarantine{}
	service := NewService(ServiceOptions{Gateway: &fakeIngestor{}, Quarantine: quarantine})
	ctx := WithIdentity(context.Background(), agentIdentity())
	env := envelope(dto.Scope{TenantID: "tenant-evil", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"text":"x"}`)
	result, err := service.Observe(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(QuarantineResult)
	if !ok || response.Reason != "cross-tenant-envelope" || len(quarantine.events) != 1 {
		t.Fatalf("result=%#v events=%#v", result, quarantine.events)
	}
}
