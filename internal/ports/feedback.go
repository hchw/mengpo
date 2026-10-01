package ports

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidFeedback = errors.New("invalid memory feedback")

// MemoryFeedback is one user/agent signal attached to an injected memory.
type MemoryFeedback struct {
	MemoryID  string
	UserID    string
	SessionID string
	Type      string
	Reason    string
	RequestID string
	CreatedAt time.Time
}

var FeedbackTypes = []string{"accepted", "helpful", "harmful", "corrected", "ignored", "conflict", "expired"}

type FeedbackRepository interface {
	StoreFeedback(ctx context.Context, tenantID string, feedback MemoryFeedback) error
}

func ValidFeedbackType(value string) bool {
	for _, candidate := range FeedbackTypes {
		if candidate == value {
			return true
		}
	}
	return false
}
