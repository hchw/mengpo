// Package agentaccess resolves the trusted tenant/principal identity for an
// Agent request and binds it to one tenant-bound Envelope. Envelope claims are
// never authorization evidence: they must match the identity resolved from a
// deployment credential (Level 0), an Agent handshake context (Level 1/2), or a
// verified console session.
package agentaccess

import (
	"context"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/domain/observation"
)

type IdentitySource string

const (
	SourceDeployment   IdentitySource = "deployment"
	SourceAgentContext IdentitySource = "agent-context"
	SourceVerifiedUser IdentitySource = "verified-user"
)

// Identity is the trusted, server-side principal. It is resolved from a
// credential or handshake response, never from the request body.
type Identity struct {
	TenantID     string
	UserID       string
	AgentID      string
	SourceID     string
	SessionID    string
	ScopeType    string
	Capabilities []string
	Source       IdentitySource
	AccessLevel  observation.AccessLevel
}

type Scoped struct {
	TenantID       string
	UserID         string
	AgentID        string
	SessionID      string
	ScopeType      string
	PrincipalType  string
	Capabilities   []string
	RequestID      string
	IdempotencyKey string
}

var (
	ErrCrossTenant = errors.New("cross-tenant envelope rejected")
	ErrForbidden   = errors.New("envelope scope is not authorized for this identity")
	ErrNoIdentity  = errors.New("no trusted identity on context")
)

// Resolver turns a credential or handshake result into a trusted Identity.
// Level 0 deployments resolve tenant from the deployment credential; Level 1/2
// resolve it from the Agent handshake context.
type Resolver interface {
	Resolve(ctx context.Context) (Identity, error)
}

type ResolverFunc func(ctx context.Context) (Identity, error)

func (f ResolverFunc) Resolve(ctx context.Context) (Identity, error) { return f(ctx) }

type contextKey struct{}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok
}

// Binder validates one Envelope against a trusted identity. It rejects any
// Envelope whose declared tenant, user, principal, agent, or session does not
// belong to the identity; it never widens scope based on Envelope claims.
type Binder struct{}

func NewBinder() *Binder { return &Binder{} }

func (b *Binder) Bind(identity Identity, envelope dto.Envelope) (Scoped, error) {
	if identity.TenantID == "" || identity.UserID == "" {
		return Scoped{}, ErrNoIdentity
	}
	switch {
	case envelope.Scope.TenantID != identity.TenantID:
		return Scoped{}, fmt.Errorf("%w: declared tenant %q", ErrCrossTenant, envelope.Scope.TenantID)
	case envelope.Scope.UserID != identity.UserID:
		return Scoped{}, fmt.Errorf("%w: declared user %q", ErrForbidden, envelope.Scope.UserID)
	}
	if envelope.Principal.Type == "agent" {
		if identity.AgentID == "" || envelope.Principal.ID != identity.AgentID {
			return Scoped{}, fmt.Errorf("%w: agent principal", ErrForbidden)
		}
	} else if envelope.Principal.Type == "user" && envelope.Principal.ID != identity.UserID {
		return Scoped{}, fmt.Errorf("%w: user principal", ErrForbidden)
	}
	scopeType := envelope.Scope.Type
	if identity.ScopeType != "" && identity.ScopeType != scopeType {
		return Scoped{}, fmt.Errorf("%w: scope %q", ErrForbidden, scopeType)
	}
	sessionID := envelope.Scope.SessionID
	if scopeType == "session" {
		if identity.SessionID != "" {
			if sessionID != identity.SessionID {
				return Scoped{}, fmt.Errorf("%w: session scope", ErrForbidden)
			}
		} else if sessionID != "" {
			// Agent contexts may bind a fresh session id for the bound user;
			// the session repository re-checks ownership before access.
			sessionID = envelope.Scope.SessionID
		}
	}
	return Scoped{
		TenantID:       identity.TenantID,
		UserID:         identity.UserID,
		AgentID:        identity.AgentID,
		SessionID:      sessionID,
		ScopeType:      scopeType,
		PrincipalType:  envelope.Principal.Type,
		Capabilities:   append([]string(nil), identity.Capabilities...),
		RequestID:      envelope.RequestID,
		IdempotencyKey: envelope.IdempotencyKey,
	}, nil
}

func (s Scoped) HasCapability(capability string) bool {
	for _, value := range s.Capabilities {
		if value == capability {
			return true
		}
	}
	return false
}
