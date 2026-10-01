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
	source    obsdomain.SourceType
	event     obsdomain.Event
	created   bool
	err       error
}

func (f *fakeIngestor) record(p observation.Principal, in observation.Input, source obsdomain.SourceType) (obsdomain.Event, bool, error) {
	f.principal = p
	f.input = in
	f.source = source
	return f.event, f.created, f.err
}

func (f *fakeIngestor) IngestMessage(_ context.Context, p observation.Principal, in observation.Input) (obsdomain.Event, bool, error) {
	return f.record(p, in, obsdomain.SourceUser)
}

func (f *fakeIngestor) IngestTool(_ context.Context, p observation.Principal, in observation.Input) (obsdomain.Event, bool, error) {
	return f.record(p, in, obsdomain.SourceTool)
}

func (f *fakeIngestor) IngestWorkflow(_ context.Context, p observation.Principal, in observation.Input) (obsdomain.Event, bool, error) {
	return f.record(p, in, obsdomain.SourceWorkflow)
}

func (f *fakeIngestor) IngestCode(_ context.Context, p observation.Principal, in observation.Input) (obsdomain.Event, bool, error) {
	return f.record(p, in, obsdomain.SourceAgent)
}

func (f *fakeIngestor) IngestGateway(_ context.Context, p observation.Principal, in observation.Input) (obsdomain.Event, bool, error) {
	return f.record(p, in, obsdomain.SourceGateway)
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

// TestObserveAdmitsVerifiedUserSession proves the console path builds a
// principal the observations gateway accepts (UserBound), so /api/v1/observe
// works for a signed-in user and not only for Agents or deployments.
func TestObserveAdmitsVerifiedUserSession(t *testing.T) {
	ingestor := &fakeIngestor{event: obsdomain.Event{ID: "event-1", TenantID: "tenant-a", SessionID: "session-a"}, created: true}
	service := NewService(ServiceOptions{Gateway: ingestor})
	identity := Identity{
		TenantID:     "tenant-a",
		UserID:       "user-a",
		SourceID:     "user-a",
		Capabilities: []string{"observe", "project", "feedback"},
		Source:       SourceVerifiedUser,
	}
	ctx := WithIdentity(context.Background(), identity)
	envelope := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "user", ID: "user-a"}, `{"text":"hello","message_type":"user_remember"}`)
	if _, err := service.Observe(ctx, envelope); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	principal := ingestor.principal
	if !principal.UserBound {
		t.Fatalf("principal is not user-bound: %#v", principal)
	}
	if principal.AgentBound {
		t.Fatalf("verified user must not be marked agent-bound: %#v", principal)
	}
	if principal.AccessLevel != obsdomain.Level1 {
		t.Fatalf("access level = %v, want level1", principal.AccessLevel)
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

// TestObserveDispatchesDeclaredSourceType proves a declared source category
// reaches the matching ingest entry point, and that an omitted category keeps
// the historical user-message path.
func TestObserveDispatchesDeclaredSourceType(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    obsdomain.SourceType
	}{
		{"omitted defaults to user", `{"text":"hello"}`, obsdomain.SourceUser},
		{"explicit user", `{"text":"hello","source_type":"user"}`, obsdomain.SourceUser},
		{"tool", `{"source_type":"tool","message_type":"tool.result"}`, obsdomain.SourceTool},
		{"workflow", `{"source_type":"workflow"}`, obsdomain.SourceWorkflow},
		{"agent", `{"source_type":"agent"}`, obsdomain.SourceAgent},
		{"gateway", `{"source_type":"gateway"}`, obsdomain.SourceGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ingestor := &fakeIngestor{event: obsdomain.Event{ID: "event-1", TenantID: "tenant-a"}, created: true}
			service := NewService(ServiceOptions{Gateway: ingestor})
			ctx := WithIdentity(context.Background(), agentIdentity())
			env := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, tc.payload)
			if _, err := service.Observe(ctx, env); err != nil {
				t.Fatal(err)
			}
			if ingestor.source != tc.want {
				t.Fatalf("source = %q, want %q", ingestor.source, tc.want)
			}
		})
	}
}

// TestObserveRejectsUnknownSourceType proves an unsupported source category is a
// payload validation failure (400), not a silent downgrade.
func TestObserveRejectsUnknownSourceType(t *testing.T) {
	ingestor := &fakeIngestor{event: obsdomain.Event{ID: "event-1"}, created: true}
	service := NewService(ServiceOptions{Gateway: ingestor})
	ctx := WithIdentity(context.Background(), agentIdentity())
	env := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, `{"source_type":"not-a-source"}`)
	_, err := service.Observe(ctx, env)
	if !errors.Is(err, dto.ErrInvalidEnvelope) {
		t.Fatalf("err = %v, want invalid envelope", err)
	}
	if ingestor.source != "" {
		t.Fatalf("rejected observation reached ingest: %q", ingestor.source)
	}
}

// TestObserveForwardsDeclaredEventTypeAndTrace proves the declared event type,
// ordering, causal link and runtime trace reach the observation boundary intact.
func TestObserveForwardsDeclaredEventTypeAndTrace(t *testing.T) {
	ingestor := &fakeIngestor{event: obsdomain.Event{ID: "event-1", TenantID: "tenant-a"}, created: true}
	service := NewService(ServiceOptions{Gateway: ingestor})
	ctx := WithIdentity(context.Background(), agentIdentity())
	payload := `{"source_type":"tool","message_type":"tool.failure","sequence":7,"parent_event_id":"00000000-0000-4000-8000-000000000009","trace":{"task_id":"task-1","attempt_id":"attempt-2","projection_id":"00000000-0000-4000-8000-000000000001","tool_result_id":"call-3","outcome_id":"outcome-4"}}`
	env := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, payload)
	if _, err := service.Observe(ctx, env); err != nil {
		t.Fatal(err)
	}
	input := ingestor.input
	if input.MessageType != "tool.failure" {
		t.Fatalf("message type = %q", input.MessageType)
	}
	if input.Sequence == nil || *input.Sequence != 7 {
		t.Fatalf("sequence = %v", input.Sequence)
	}
	if input.ParentEventID != "00000000-0000-4000-8000-000000000009" {
		t.Fatalf("parent event = %q", input.ParentEventID)
	}
	trace := input.Trace
	if trace.TaskID != "task-1" || trace.AttemptID != "attempt-2" || trace.ToolResultID != "call-3" || trace.OutcomeID != "outcome-4" || trace.ProjectionID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("trace = %#v", trace)
	}
}

type fakeProjectionLookup struct {
	lookup ports.ProjectionLookup
	err    error
	calls  int
}

func (f *fakeProjectionLookup) FindProjection(_ context.Context, _, _ string) (ports.ProjectionLookup, error) {
	f.calls++
	return f.lookup, f.err
}

func observeWithTrace(t *testing.T, service *Service, payload string) *fakeIngestor {
	t.Helper()
	ingestor := &fakeIngestor{event: obsdomain.Event{ID: "event-1", TenantID: "tenant-a"}, created: true}
	service.gateway = ingestor
	ctx := WithIdentity(context.Background(), agentIdentity())
	env := envelope(dto.Scope{TenantID: "tenant-a", UserID: "user-a", Type: "session", SessionID: "session-a"}, dto.Principal{Type: "agent", ID: "agent-a"}, payload)
	if _, err := service.Observe(ctx, env); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	return ingestor
}

const projectionTracePayload = `{"source_type":"tool","trace":{"projection_id":"00000000-0000-4000-8000-000000000001","tool_result_id":"call-1"}}`

// TestObserveDerivesUsedMemoryIDs proves used-memory identity comes from the
// projection the caller referenced, not from the caller's own claim.
func TestObserveDerivesUsedMemoryIDs(t *testing.T) {
	lookup := &fakeProjectionLookup{lookup: ports.ProjectionLookup{
		ID:                "00000000-0000-4000-8000-000000000001",
		UserID:            "user-a",
		SessionID:         "session-a",
		SelectedMemoryIDs: []string{"memory-1", "memory-2"},
	}}
	service := NewService(ServiceOptions{Projections: lookup})
	ingestor := observeWithTrace(t, service, projectionTracePayload)
	if got := ingestor.input.Trace.UsedMemoryIDs; len(got) != 2 || got[0] != "memory-1" || got[1] != "memory-2" {
		t.Fatalf("used memory ids = %v, want derived from projection", got)
	}
	if lookup.calls != 1 {
		t.Fatalf("lookup calls = %d, want 1", lookup.calls)
	}
}

// TestObserveKeepsCallerDeclaredUsedMemoryIDs proves an explicit set wins and the
// projection is not consulted.
func TestObserveKeepsCallerDeclaredUsedMemoryIDs(t *testing.T) {
	lookup := &fakeProjectionLookup{lookup: ports.ProjectionLookup{UserID: "user-a", SelectedMemoryIDs: []string{"memory-1"}}}
	service := NewService(ServiceOptions{Projections: lookup})
	payload := `{"trace":{"projection_id":"00000000-0000-4000-8000-000000000001","used_memory_ids":["memory-explicit"]}}`
	ingestor := observeWithTrace(t, service, payload)
	if got := ingestor.input.Trace.UsedMemoryIDs; len(got) != 1 || got[0] != "memory-explicit" {
		t.Fatalf("used memory ids = %v, want the caller's set", got)
	}
	if lookup.calls != 0 {
		t.Fatalf("lookup calls = %d, want 0 when the caller declared its own set", lookup.calls)
	}
}

// TestObserveIgnoresProjectionItCannotOwn proves a reference owned by another
// user, or attached to another session, never contributes used-memory identity
// and never fails the observation.
func TestObserveIgnoresProjectionItCannotOwn(t *testing.T) {
	cases := []struct {
		name   string
		lookup ports.ProjectionLookup
	}{
		{"another user", ports.ProjectionLookup{UserID: "user-b", SessionID: "session-a", SelectedMemoryIDs: []string{"memory-1"}}},
		{"another session", ports.ProjectionLookup{UserID: "user-a", SessionID: "session-z", SelectedMemoryIDs: []string{"memory-1"}}},
		{"no selected memories", ports.ProjectionLookup{UserID: "user-a", SessionID: "session-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &fakeProjectionLookup{lookup: tc.lookup}
			service := NewService(ServiceOptions{Projections: lookup})
			ingestor := observeWithTrace(t, service, projectionTracePayload)
			if got := ingestor.input.Trace.UsedMemoryIDs; len(got) != 0 {
				t.Fatalf("used memory ids = %v, want none", got)
			}
		})
	}
}

// TestObserveIgnoresUnresolvableProjection proves an unknown reference is not
// fabricated into used-memory identity, and does not fail the observation.
func TestObserveIgnoresUnresolvableProjection(t *testing.T) {
	lookup := &fakeProjectionLookup{err: ports.ErrProjectionNotFound}
	service := NewService(ServiceOptions{Projections: lookup})
	ingestor := observeWithTrace(t, service, projectionTracePayload)
	if got := ingestor.input.Trace.UsedMemoryIDs; len(got) != 0 {
		t.Fatalf("used memory ids = %v, want none", got)
	}
	if ingestor.input.Trace.ProjectionID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("projection reference = %q, want it preserved", ingestor.input.Trace.ProjectionID)
	}
}
