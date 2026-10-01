package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

type FeedbackRepository struct{ router *tenantdb.Router }

func NewFeedbackRepository(router *tenantdb.Router) *FeedbackRepository {
	return &FeedbackRepository{router: router}
}

// StoreFeedback records one feedback signal in the tenant schema. Feedback is
// evidence about a memory; it never mutates the memory content directly.
func (r *FeedbackRepository) StoreFeedback(ctx context.Context, tenantID string, feedback ports.MemoryFeedback) error {
	if tenantID == "" || feedback.MemoryID == "" || feedback.UserID == "" || feedback.RequestID == "" || !ports.ValidFeedbackType(feedback.Type) {
		return ports.ErrInvalidFeedback
	}
	if feedback.CreatedAt.IsZero() {
		feedback.CreatedAt = time.Now().UTC()
	}
	feedbackID, err := newFeedbackID()
	if err != nil {
		return err
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_feedback (id, memory_id, user_id, session_id, feedback_type, reason, request_id, created_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4,'')::uuid, $5, NULLIF($6,''), $7, $8)`,
			feedbackID, feedback.MemoryID, feedback.UserID, feedback.SessionID, feedback.Type, feedback.Reason, feedback.RequestID, feedback.CreatedAt)
		if err != nil {
			return fmt.Errorf("store feedback: %w", err)
		}
		return nil
	})
}

func newFeedbackID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
