package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// AuditRepository appends audit entries to the tenant's audit_events table.
type AuditRepository struct {
	router *tenantdb.Router
}

func NewAuditRepository(router *tenantdb.Router) *AuditRepository {
	return &AuditRepository{router: router}
}

func (r *AuditRepository) RecordAuditEvent(ctx context.Context, tenantID string, event ports.AuditEventRecord) error {
	if tenantID == "" || event.Action == "" || event.ResourceType == "" {
		return errors.New("audit event requires tenant, action and resource type")
	}
	changes := event.Changes
	if len(changes) == 0 {
		changes = json.RawMessage(`{}`)
	}
	actorType := event.ActorType
	if actorType == "" {
		actorType = "user"
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO audit_events (id, actor_type, actor_id, action, resource_type, resource_id, request_id, changes)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7::jsonb)`,
			actorType, event.ActorID, event.Action, event.ResourceType, event.ResourceID, event.RequestID, string(changes))
		return err
	})
}

var _ ports.AuditWriter = (*AuditRepository)(nil)
