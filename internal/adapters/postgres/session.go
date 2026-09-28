package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// SessionRepository provides tenant-scoped session ownership checks used by
// memory-scope authorization.
type SessionRepository struct {
	router *tenantdb.Router
}

func NewSessionRepository(router *tenantdb.Router) *SessionRepository {
	return &SessionRepository{router: router}
}

func (r *SessionRepository) GetSessionOwner(ctx context.Context, tenantID, sessionID string) (string, error) {
	if tenantID == "" || sessionID == "" {
		return "", ports.ErrSessionNotFound
	}
	var ownerID string
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id = $1`, sessionID).Scan(&ownerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("get session owner: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return ownerID, nil
}
