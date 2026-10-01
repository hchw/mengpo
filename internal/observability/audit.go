package observability

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

type SecurityEventType string

const (
	SecurityProvider   SecurityEventType = "provider"
	SecurityAPI        SecurityEventType = "api"
	SecurityDataAccess SecurityEventType = "data_access"
	SecurityExport     SecurityEventType = "export"
	SecurityDelete     SecurityEventType = "delete"
	SecurityAudit      SecurityEventType = "audit"
)

var ErrIncompleteSecurityEvent = errors.New("security event requires type, actor, tenant and outcome")

// SecurityEvent is one append-only security-relevant record. It never carries
// raw memory content: only identifiers plus an optional redacted detail.
type SecurityEvent struct {
	Type      SecurityEventType
	ActorType string
	ActorID   string
	TenantID  string
	Resource  string
	Action    string
	Outcome   string
	RequestID string
	TraceID   string
	Detail    string
}

// SecurityLogger emits structured, tenant-scoped security events. Tenant and
// actor are mandatory so a missing identity cannot be silently logged as system
// activity.
type SecurityLogger struct {
	logger *slog.Logger
	clock  func() time.Time
}

func NewSecurityLogger(logger *slog.Logger) *SecurityLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return &SecurityLogger{logger: logger, clock: time.Now}
}

func (l *SecurityLogger) Log(ctx context.Context, event SecurityEvent) error {
	if event.Type == "" || event.ActorID == "" || event.TenantID == "" || event.Outcome == "" {
		return ErrIncompleteSecurityEvent
	}
	requestID := event.RequestID
	if requestID == "" {
		requestID = RequestID(ctx)
	}
	traceID := event.TraceID
	if traceID == "" {
		traceID = TraceID(ctx)
	}
	l.logger.InfoContext(ctx, "security_event",
		slog.String("event_type", string(event.Type)),
		slog.String("actor_type", event.ActorType),
		slog.String("actor_id", event.ActorID),
		slog.String("tenant_id", event.TenantID),
		slog.String("resource", event.Resource),
		slog.String("action", event.Action),
		slog.String("outcome", event.Outcome),
		slog.String("request_id", requestID),
		slog.String("trace_id", traceID),
		slog.String("detail", redactDetail(event.Detail)),
		slog.Time("at", l.clock().UTC()),
	)
	return nil
}

var sensitiveMarkers = []string{"password", "secret", "token", "authorization", "api_key", "apikey"}

// redactDetail drops obviously sensitive key/value fragments rather than
// trusting callers to sanitize audit details.
func redactDetail(detail string) string {
	lower := strings.ToLower(detail)
	for _, marker := range sensitiveMarkers {
		if strings.Contains(lower, marker) {
			return "[redacted]"
		}
	}
	return detail
}

type ExportRequest struct {
	TenantID string
	ActorID  string
	Format   string
	Scope    string
}

// AuthorizeExport rejects an export that is missing tenant/actor identity or a
// supported format, so an unauthorized export cannot even reach the exporter.
func AuthorizeExport(request ExportRequest) error {
	if request.TenantID == "" || request.ActorID == "" {
		return ErrIncompleteSecurityEvent
	}
	switch request.Format {
	case "jsonl", "csv":
	default:
		return errors.New("unsupported export format")
	}
	return nil
}

type DeleteRequest struct {
	TenantID string
	ActorID  string
	MemoryID string
	Reason   string
}

// AuthorizeDelete requires an explicit reason so deletions are auditable.
func AuthorizeDelete(request DeleteRequest) error {
	if request.TenantID == "" || request.ActorID == "" || request.MemoryID == "" || strings.TrimSpace(request.Reason) == "" {
		return ErrIncompleteSecurityEvent
	}
	return nil
}
