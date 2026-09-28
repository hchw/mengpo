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
