package ports

import (
	"context"
	"errors"
)

var ErrSessionNotFound = errors.New("session not found")

// SessionScopeRepository resolves a session owner only within the tenant schema
// selected by the caller's trusted tenant context.
type SessionScopeRepository interface {
	GetSessionOwner(ctx context.Context, tenantID, sessionID string) (string, error)
}

// SessionBinding records the agent that opened a session and returns the
// canonical session id. A repeated bind for the same external identity must be
// idempotent so Agent retries never create duplicate sessions.
type SessionBinding struct {
	TenantID   string
	SessionID  string
	UserID     string
	AgentID    string
	ExternalID string
	Title      string
}

type SessionBinder interface {
	BindSession(ctx context.Context, binding SessionBinding) (string, error)
}
