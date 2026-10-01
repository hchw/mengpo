package agentaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/observation"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

type recordingStore struct{ stored obsdomain.Event }

func (r *recordingStore) StoreObservation(_ context.Context, event obsdomain.Event) (obsdomain.Event, bool, error) {
	r.stored = event
	return event, true, nil
}

func TestLevel0DeploymentCredentialResolvesTenantAndIngests(t *testing.T) {
	identity, err := ResolveDeployment(DeploymentConfig{TenantID: "tenant-deploy", ServiceID: "svc-1", AllowedScopes: []string{"session"}}, RequestContext{UserID: "user-1", SessionID: "session-1", ScopeType: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.TenantID != "tenant-deploy" || identity.Source != SourceDeployment || identity.AccessLevel != obsdomain.Level0 {
		t.Fatalf("identity=%#v", identity)
	}
	principal := PrincipalFor(identity, Scoped{TenantID: identity.TenantID, UserID: identity.UserID})
	if !principal.DeploymentBound || principal.AccessLevel != obsdomain.Level0 || principal.TenantID != "tenant-deploy" {
		t.Fatalf("principal=%#v", principal)
	}
	store := &recordingStore{}
	gateway := observation.NewGateway(store)
	ctx := WithIdentity(context.Background(), identity)
	service := NewService(ServiceOptions{Gateway: gateway})
	env := envelope(dto.Scope{TenantID: "tenant-deploy", UserID: "user-1", Type: "session", SessionID: "session-1"}, dto.Principal{Type: "user", ID: "user-1"}, `{"text":"hello","payload":{"k":"v"}}`)
	if _, err := service.Observe(ctx, env); err != nil {
		t.Fatalf("level 0 observe: %v", err)
	}
	if store.stored.TenantID != "tenant-deploy" || store.stored.AccessLevel != obsdomain.Level0 {
		t.Fatalf("stored event=%#v", store.stored)
	}
}

func TestLevel1And2AgentContextResolveTenantFromHandshake(t *testing.T) {
	context1 := AgentContext{TenantID: "tenant-agent", AgentID: "agent-1", AllowedCapabilities: []string{"observe"}, AllowedSessionIDs: []string{"session-1"}, AllowedScopeTypes: []string{"session"}}
	level1, err := ResolveAgentContext(context1, RequestContext{UserID: "user-1", SessionID: "session-1", ScopeType: "session"}, obsdomain.Level1)
	if err != nil || level1.TenantID != "tenant-agent" || level1.Source != SourceAgentContext || level1.AccessLevel != obsdomain.Level1 {
		t.Fatalf("level1=%#v err=%v", level1, err)
	}
	level2, err := ResolveAgentContext(context1, RequestContext{UserID: "user-1", SessionID: "session-1", ScopeType: "session"}, obsdomain.Level2)
	if err != nil || level2.AccessLevel != obsdomain.Level2 {
		t.Fatalf("level2=%#v err=%v", level2, err)
	}
	principal := PrincipalFor(level1, Scoped{TenantID: level1.TenantID, UserID: level1.UserID})
	if !principal.AgentBound || principal.DeploymentBound {
		t.Fatalf("principal=%#v", principal)
	}
	if _, err := ResolveAgentContext(context1, RequestContext{UserID: "user-1", SessionID: "session-x", ScopeType: "session"}, obsdomain.Level1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized session err=%v", err)
	}
}

func TestResolversRejectEmptyIdentityAndUnpermittedUser(t *testing.T) {
	if _, err := ResolveDeployment(DeploymentConfig{}, RequestContext{UserID: "u"}); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("empty deployment err=%v", err)
	}
	if _, err := ResolveDeployment(DeploymentConfig{TenantID: "t", AllowedUserIDs: []string{"other"}}, RequestContext{UserID: "u"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unpermitted user err=%v", err)
	}
	if _, err := ResolveAgentContext(AgentContext{}, RequestContext{UserID: "u"}, obsdomain.Level1); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("empty agent context err=%v", err)
	}
}

func TestEnvelopeCannotSelfDeclareTenantAcrossLevels(t *testing.T) {
	identity, _ := ResolveDeployment(DeploymentConfig{TenantID: "tenant-deploy", ServiceID: "svc"}, RequestContext{UserID: "user-1", SessionID: "session-1", ScopeType: "session"})
	ctx := WithIdentity(context.Background(), identity)
	service := NewService(ServiceOptions{Gateway: &fakeIngestor{}})
	env := envelope(dto.Scope{TenantID: "tenant-other", UserID: "user-1", Type: "session", SessionID: "session-1"}, dto.Principal{Type: "user", ID: "user-1"}, `{"text":"x"}`)
	if _, err := service.Observe(ctx, env); !errors.Is(err, ErrCrossTenant) {
		t.Fatalf("err=%v want cross-tenant (no quarantine sink)", err)
	}
	quarantine := &fakeQuarantine{}
	service = NewService(ServiceOptions{Gateway: &fakeIngestor{}, Quarantine: quarantine})
	if _, err := service.Observe(ctx, env); err != nil || len(quarantine.events) != 1 {
		t.Fatalf("quarantine err=%v events=%#v", err, quarantine.events)
	}
	if quarantine.events[0].DeclaredTenant != "tenant-other" {
		t.Fatalf("quarantined declared tenant=%q", quarantine.events[0].DeclaredTenant)
	}
	_ = ports.QuarantinedEvent{}
}
