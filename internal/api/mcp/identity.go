// Package mcpserver exposes Mengpo's agent-facing memory capabilities as MCP
// tools over the streamable HTTP transport. It depends only on the request DTO
// and defines its own narrow boundary types, so it never imports the
// application layer; the assembly layer binds those boundaries to the real
// services.
package mcpserver

import (
	"context"

	"github.com/hchw/mengpo/internal/api/dto"
)

// UseCases is the memory command surface the MCP tools dispatch to. It mirrors
// the HTTP boundary so both transports drive the same application services.
type UseCases interface {
	Observe(context.Context, dto.Envelope) (any, error)
	Project(context.Context, dto.Envelope) (any, error)
	Consolidate(context.Context, dto.Envelope) (any, error)
	Feedback(context.Context, dto.Envelope) (any, error)
	Session(context.Context, dto.Envelope) (any, error)
}

// Identity is the trusted caller resolved from an agent credential. It is a
// transport-local value: the application layer reconstructs its own identity
// from it in the assembly adapter.
type Identity struct {
	TenantID     string
	UserID       string
	AgentID      string
	Capabilities []string
}

// HasCapability reports whether the identity was granted the capability.
func (i Identity) HasCapability(capability string) bool {
	for _, value := range i.Capabilities {
		if value == capability {
			return true
		}
	}
	return false
}

// ResolveFunc turns an inbound credential into a trusted identity. A non-nil
// error means the credential is missing or invalid.
type ResolveFunc func(ctx context.Context, credential string) (Identity, error)

type identityKey struct{}

// WithIdentity attaches the resolved identity to a context so tool handlers and
// the assembly adapter can read it without importing application types.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFromContext returns the resolved identity attached by WithIdentity.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}
