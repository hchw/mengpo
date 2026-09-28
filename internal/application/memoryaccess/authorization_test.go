package memoryaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

type sessionOwners map[string]map[string]string

func (owners sessionOwners) GetSessionOwner(_ context.Context, tenantID, sessionID string) (string, error) {
	if owner, ok := owners[tenantID][sessionID]; ok {
		return owner, nil
	}
	return "", ports.ErrSessionNotFound
}

func TestAuthorizeUserScopesAndTenantBoundary(t *testing.T) {
	service := NewService(sessionOwners{"tenant-a": {"session-1": "user-1", "session-other": "user-2"}})
	active := tenants.ActiveTenantContext{
		UserID: "user-1", MembershipID: "membership-1",
		Tenant: auth.Tenant{ID: "tenant-a", Status: auth.TenantActive},
	}

	global, err := service.AuthorizeUser(context.Background(), active, ScopeRequest{
		TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeUserGlobal,
	})
	if err != nil {
		t.Fatalf("authorize User Global Memory: %v", err)
	}
	if global.ScopeID != "user-1" || global.ScopeType != memory.ScopeUserGlobal || len(global.AllowedScopeTypes) != 1 {
		t.Fatalf("global authorization grant = %#v", global)
	}

	session, err := service.AuthorizeUser(context.Background(), active, ScopeRequest{
		TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeSession,
		SessionID: "session-1", WorkflowID: "workflow-ignored-for-scope",
	})
	if err != nil {
		t.Fatalf("authorize owned Session Memory: %v", err)
	}
	if session.ScopeID != "session-1" || session.WorkflowID != "workflow-ignored-for-scope" || len(session.AllowedScopeTypes) != 2 {
		t.Fatalf("session authorization grant = %#v", session)
	}

	for _, request := range []ScopeRequest{
		{TenantID: "tenant-b", UserID: "user-1", ScopeType: memory.ScopeUserGlobal},
		{TenantID: "tenant-a", UserID: "user-2", ScopeType: memory.ScopeUserGlobal},
		{TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeSession, SessionID: "session-other"},
		{TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeSession, SessionID: "missing-session"},
	} {
		if _, err := service.AuthorizeUser(context.Background(), active, request); !errors.Is(err, ErrForbidden) {
			t.Errorf("unauthorized request %#v error = %v, want ErrForbidden", request, err)
		}
	}
	if _, err := service.AuthorizeUser(context.Background(), active, ScopeRequest{
		TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeType("workflow"), WorkflowID: "workflow-1",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("workflow scope error = %v, want ErrInvalidRequest", err)
	}
}

func TestAuthorizeAgentRestrictsConfiguredUsersSessionsAndScopes(t *testing.T) {
	service := NewService(sessionOwners{"tenant-a": {"session-1": "user-1", "session-2": "user-1", "session-other": "user-2"}})
	agent := auth.Agent{
		ID: "agent-1", TenantID: "tenant-a", Status: auth.AgentActive,
		AllowedUserIDs: []string{"user-1"}, AllowedSessionIDs: []string{"session-1"},
		AllowedScopes: []string{string(memory.ScopeUserGlobal), string(memory.ScopeSession)},
	}
	request := ScopeRequest{
		TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeSession,
		SessionID: "session-1", WorkflowID: "workflow-1",
	}
	grant, err := service.AuthorizeAgent(context.Background(), agent, request)
	if err != nil {
		t.Fatalf("authorize Agent session scope: %v", err)
	}
	if grant.PrincipalType != "agent" || grant.AgentID != agent.ID || grant.WorkflowID != request.WorkflowID ||
		grant.ScopeType != memory.ScopeSession || grant.ScopeID != request.SessionID {
		t.Fatalf("Agent grant incorrectly models caller metadata as scope: %#v", grant)
	}

	for _, denied := range []ScopeRequest{
		{TenantID: "tenant-b", UserID: "user-1", ScopeType: memory.ScopeSession, SessionID: "session-1"},
		{TenantID: "tenant-a", UserID: "user-2", ScopeType: memory.ScopeSession, SessionID: "session-other"},
		{TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeSession, SessionID: "session-2"},
		{TenantID: "tenant-a", UserID: "user-1", ScopeType: memory.ScopeType("workflow"), WorkflowID: "workflow-1"},
	} {
		if _, err := service.AuthorizeAgent(context.Background(), agent, denied); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("unauthorized Agent request %#v error = %v", denied, err)
		}
	}

	inactive := agent
	inactive.Status = auth.AgentDisabled
	if _, err := service.AuthorizeAgent(context.Background(), inactive, request); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("disabled Agent error = %v, want ErrInvalidGrant", err)
	}
}

func TestAuthorizedScopeOnlyAllowsItsUserAndMemoryDimensions(t *testing.T) {
	scope := AuthorizedScope{
		TenantID: "tenant-a", UserID: "user-1", SessionID: "session-1",
		AllowedScopeTypes: []memory.ScopeType{memory.ScopeUserGlobal, memory.ScopeSession},
	}
	allowed := []ports.MemoryNodeRecord{
		{UserID: "user-1", ScopeType: string(memory.ScopeUserGlobal), ScopeID: "user-1"},
		{UserID: "user-1", ScopeType: string(memory.ScopeSession), ScopeID: "session-1", SessionID: "session-1"},
	}
	for _, node := range allowed {
		if !scope.AllowsMemory(node) {
			t.Errorf("scope rejected authorized memory %#v", node)
		}
	}
	denied := []ports.MemoryNodeRecord{
		{UserID: "user-2", ScopeType: string(memory.ScopeUserGlobal), ScopeID: "user-2"},
		{UserID: "user-1", ScopeType: "workflow", ScopeID: "workflow-1"},
		{UserID: "user-1", ScopeType: string(memory.ScopeSession), ScopeID: "session-2", SessionID: "session-2"},
	}
	for _, node := range denied {
		if scope.AllowsMemory(node) {
			t.Errorf("scope admitted unauthorized memory %#v", node)
		}
	}
	sessionOnly := scope
	sessionOnly.AllowedScopeTypes = []memory.ScopeType{memory.ScopeSession}
	filtered := sessionOnly.FilterMemoryNodes(append(allowed, denied...))
	if len(filtered) != 1 || filtered[0].ScopeType != string(memory.ScopeSession) {
		t.Fatalf("session-only grant returned mixed scopes: %#v", filtered)
	}
}
