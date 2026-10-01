package ports

import (
	"context"
	"errors"
	"time"
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

// SessionListRequest filters sessions for the console.
type SessionListRequest struct {
	UserID   string
	Statuses []string
	Page     int
	PageSize int
}

// SessionSummaryRecord is one session row for the console session explorer.
type SessionSummaryRecord struct {
	ID        string
	Title     string
	Status    string
	StartedAt time.Time
	UpdatedAt time.Time
}

// SessionListPage is one paginated page of sessions.
type SessionListPage struct {
	Items    []SessionSummaryRecord
	Total    int64
	Page     int
	PageSize int
}

// SessionListRepository lists tenant sessions for the console. It is separate
// from the agent SessionBinder because the command endpoint owns
// POST /api/v1/sessions.
type SessionListRepository interface {
	ListSessions(ctx context.Context, tenantID string, request SessionListRequest) (SessionListPage, error)
}
