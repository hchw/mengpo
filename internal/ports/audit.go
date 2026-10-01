package ports

import (
	"context"
	"encoding/json"
)

// AuditEventRecord is one durable audit entry. It never carries secret values.
type AuditEventRecord struct {
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	RequestID    string
	Changes      json.RawMessage
}

// AuditWriter persists audit entries in the caller's tenant schema.
type AuditWriter interface {
	RecordAuditEvent(ctx context.Context, tenantID string, event AuditEventRecord) error
}
