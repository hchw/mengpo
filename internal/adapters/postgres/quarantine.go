package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hchw/mengpo/internal/ports"
)

// QuarantineRepository writes unbound observation envelopes to the
// platform-level quarantine table. It intentionally has no tenant routing:
// quarantined data is never written into a tenant schema.
type QuarantineRepository struct{ db *sql.DB }

func NewQuarantineRepository(db *sql.DB) *QuarantineRepository { return &QuarantineRepository{db: db} }

func (r *QuarantineRepository) QuarantineEvent(ctx context.Context, event ports.QuarantinedEvent) error {
	if event.ID == "" || event.Reason == "" {
		return fmt.Errorf("quarantine requires id and reason")
	}
	payload := event.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO quarantined_events
		(id, reason, declared_tenant, principal_type, principal_id, request_id, credential_fingerprint, payload)
		VALUES ($1::uuid, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), $8::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		event.ID, event.Reason, event.DeclaredTenant, event.PrincipalType, event.PrincipalID, event.RequestID, event.CredentialFingerprint, string(payload))
	if err != nil {
		return fmt.Errorf("quarantine event: %w", err)
	}
	return nil
}
