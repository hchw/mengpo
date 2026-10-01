package agentaccess

import (
	"fmt"

	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
)

// RequestContext carries the request-supplied identity fields that a resolver
// may constrain but never self-certify.
type RequestContext struct {
	UserID    string
	SessionID string
	ScopeType string
	AgentID   string
}

// DeploymentConfig is the operator-controlled, Level 0 bypass credential. The
// tenant comes from deployment configuration, not from the request.
type DeploymentConfig struct {
	TenantID       string
	ServiceID      string
	AllowedUserIDs []string
	AllowedScopes  []string
	Capabilities   []string
}

// ResolveDeployment implements tenant resolution for a Level 0 bypass gateway:
// the tenant is bound from the deployment credential and the request can only
// narrow the permitted user/scope.
func ResolveDeployment(config DeploymentConfig, request RequestContext) (Identity, error) {
	if config.TenantID == "" || request.UserID == "" {
		return Identity{}, fmt.Errorf("%w: deployment credential requires tenant and user", ErrNoIdentity)
	}
	if len(config.AllowedUserIDs) > 0 && !contains(config.AllowedUserIDs, request.UserID) {
		return Identity{}, fmt.Errorf("%w: user not permitted by deployment credential", ErrForbidden)
	}
	if request.ScopeType != "" && len(config.AllowedScopes) > 0 && !contains(config.AllowedScopes, request.ScopeType) {
		return Identity{}, fmt.Errorf("%w: scope not permitted by deployment credential", ErrForbidden)
	}
	capabilities := append([]string(nil), config.Capabilities...)
	if len(capabilities) == 0 {
		capabilities = []string{"observe", "project", "feedback"}
	}
	return Identity{
		TenantID:     config.TenantID,
		UserID:       request.UserID,
		SourceID:     config.ServiceID,
		SessionID:    request.SessionID,
		ScopeType:    request.ScopeType,
		Capabilities: capabilities,
		Source:       SourceDeployment,
		AccessLevel:  obsdomain.Level0,
	}, nil
}

// AgentContext is the minimum handshake result needed to resolve Level 1/2
// tenant context. It mirrors agents.HandshakeResponse without importing the
// handshake package into the request path.
type AgentContext struct {
	TenantID            string
	AgentID             string
	AllowedCapabilities []string
	AllowedSessionIDs   []string
	AllowedScopeTypes   []string
}

// ResolveAgentContext implements tenant resolution for Level 1/2 agents: the
// tenant comes from the verified handshake context, and user/session claims are
// checked against the agent's negotiated permissions.
func ResolveAgentContext(context AgentContext, request RequestContext, level obsdomain.AccessLevel) (Identity, error) {
	if context.TenantID == "" || context.AgentID == "" || request.UserID == "" {
		return Identity{}, fmt.Errorf("%w: agent context requires tenant, agent and user", ErrNoIdentity)
	}
	requestAgent := request.AgentID
	if requestAgent == "" {
		requestAgent = context.AgentID
	}
	if requestAgent != context.AgentID {
		return Identity{}, fmt.Errorf("%w: agent identity mismatch", ErrForbidden)
	}
	if request.ScopeType != "" && len(context.AllowedScopeTypes) > 0 && !contains(context.AllowedScopeTypes, request.ScopeType) {
		return Identity{}, fmt.Errorf("%w: scope not negotiated by agent", ErrForbidden)
	}
	if request.ScopeType == "session" && request.SessionID != "" && len(context.AllowedSessionIDs) > 0 && !contains(context.AllowedSessionIDs, request.SessionID) {
		return Identity{}, fmt.Errorf("%w: session not permitted by agent", ErrForbidden)
	}
	if level != obsdomain.Level1 && level != obsdomain.Level2 {
		level = obsdomain.Level1
	}
	return Identity{
		TenantID:     context.TenantID,
		UserID:       request.UserID,
		AgentID:      context.AgentID,
		SourceID:     context.AgentID,
		SessionID:    request.SessionID,
		ScopeType:    request.ScopeType,
		Capabilities: append([]string(nil), context.AllowedCapabilities...),
		Source:       SourceAgentContext,
		AccessLevel:  level,
	}, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
