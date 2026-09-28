// Package memoryaccess resolves tenant-bound user and Agent grants for the only
// supported memory scopes: user-global and session.
package memoryaccess

import (
	"context"
	"errors"
	"slices"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

var (
	ErrForbidden      = errors.New("memory scope access denied")
	ErrInvalidRequest = errors.New("invalid memory scope request")
	ErrInvalidGrant   = errors.New("invalid memory scope grant")
)

type ScopeRequest struct {
	TenantID   string
	UserID     string
	ScopeType  memory.ScopeType
	SessionID  string
	WorkflowID string // trace metadata only; never a memory scope
}

type AuthorizedScope struct {
	TenantID          string
	UserID            string
	ScopeType         memory.ScopeType
	ScopeID           string
	SessionID         string
	AllowedScopeTypes []memory.ScopeType
	PrincipalType     string
	PrincipalID       string
	AgentID           string // caller metadata only
	WorkflowID        string // trace metadata only
}

// SessionOwnerRepository resolves a session inside the selected tenant schema.
type SessionOwnerRepository interface {
	GetSessionOwner(ctx context.Context, tenantID, sessionID string) (string, error)
}

type Service struct {
	sessions SessionOwnerRepository
}

func NewService(sessions SessionOwnerRepository) *Service {
	return &Service{sessions: sessions}
}

// AuthorizeUser binds a request to a freshly resolved ActiveTenantContext. Users
// may access their own tenant-local User Global Memory and their own sessions.
func (s *Service) AuthorizeUser(ctx context.Context, active tenants.ActiveTenantContext, request ScopeRequest) (AuthorizedScope, error) {
	if s == nil || s.sessions == nil || active.UserID == "" || active.MembershipID == "" ||
		active.Tenant.ID == "" || active.Tenant.Status != auth.TenantActive {
		return AuthorizedScope{}, ErrInvalidGrant
	}
	if request.TenantID != active.Tenant.ID || request.UserID != active.UserID {
		return AuthorizedScope{}, ErrForbidden
	}
	if err := s.authorizeRequestedScope(ctx, active.Tenant.ID, request); err != nil {
		return AuthorizedScope{}, err
	}
	allowed := []memory.ScopeType{memory.ScopeUserGlobal}
	if request.ScopeType == memory.ScopeSession {
		allowed = append(allowed, memory.ScopeSession)
	}
	return AuthorizedScope{
		TenantID: active.Tenant.ID, UserID: active.UserID,
		ScopeType: request.ScopeType, ScopeID: scopeID(request), SessionID: request.SessionID,
		AllowedScopeTypes: allowed, PrincipalType: "user", PrincipalID: active.UserID,
		WorkflowID: request.WorkflowID,
	}, nil
}

// AuthorizeAgent applies the trusted, tenant-bound Agent registration to a
// requested user/session. Agent and Workflow IDs are retained only as metadata.
func (s *Service) AuthorizeAgent(ctx context.Context, agent auth.Agent, request ScopeRequest) (AuthorizedScope, error) {
	if s == nil || s.sessions == nil || agent.ID == "" || agent.TenantID == "" || agent.Status != auth.AgentActive {
		return AuthorizedScope{}, ErrInvalidGrant
	}
	if request.TenantID != agent.TenantID || request.UserID == "" ||
		!slices.Contains(agent.AllowedUserIDs, request.UserID) ||
		!slices.Contains(agent.AllowedScopes, string(request.ScopeType)) {
		return AuthorizedScope{}, ErrForbidden
	}
	if request.ScopeType == memory.ScopeSession && !slices.Contains(agent.AllowedSessionIDs, request.SessionID) {
		return AuthorizedScope{}, ErrForbidden
	}
	if err := s.authorizeRequestedScope(ctx, agent.TenantID, request); err != nil {
		return AuthorizedScope{}, err
	}
	allowed := make([]memory.ScopeType, 0, 2)
	for _, scopeType := range []memory.ScopeType{memory.ScopeUserGlobal, memory.ScopeSession} {
		if slices.Contains(agent.AllowedScopes, string(scopeType)) {
			allowed = append(allowed, scopeType)
		}
	}
	return AuthorizedScope{
		TenantID: agent.TenantID, UserID: request.UserID,
		ScopeType: request.ScopeType, ScopeID: scopeID(request), SessionID: request.SessionID,
		AllowedScopeTypes: allowed, PrincipalType: "agent", PrincipalID: agent.ID,
		AgentID: agent.ID, WorkflowID: request.WorkflowID,
	}, nil
}

func (s *Service) authorizeRequestedScope(ctx context.Context, tenantID string, request ScopeRequest) error {
	switch request.ScopeType {
	case memory.ScopeUserGlobal:
		if request.SessionID != "" {
			return ErrInvalidRequest
		}
	case memory.ScopeSession:
		if request.SessionID == "" {
			return ErrInvalidRequest
		}
		ownerID, err := s.sessions.GetSessionOwner(ctx, tenantID, request.SessionID)
		if errors.Is(err, ports.ErrSessionNotFound) || err == nil && ownerID != request.UserID {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

// FilterMemoryNodes is the final application-boundary guard for repositories
// that return mixed User Global and Session candidates.
func (scope AuthorizedScope) FilterMemoryNodes(nodes []ports.MemoryNodeRecord) []ports.MemoryNodeRecord {
	filtered := make([]ports.MemoryNodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if scope.AllowsMemory(node) {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func (scope AuthorizedScope) AllowsMemory(node ports.MemoryNodeRecord) bool {
	if scope.TenantID == "" || scope.UserID == "" || node.UserID != scope.UserID ||
		!slices.Contains(scope.AllowedScopeTypes, memory.ScopeType(node.ScopeType)) {
		return false
	}
	switch memory.ScopeType(node.ScopeType) {
	case memory.ScopeUserGlobal:
		return node.ScopeID == scope.UserID && node.SessionID == ""
	case memory.ScopeSession:
		return scope.SessionID != "" && node.ScopeID == scope.SessionID && node.SessionID == scope.SessionID
	default:
		return false
	}
}

func scopeID(request ScopeRequest) string {
	if request.ScopeType == memory.ScopeSession {
		return request.SessionID
	}
	return request.UserID
}
