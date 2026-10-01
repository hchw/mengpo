package ports

import (
	"context"
	"errors"

	"github.com/hchw/mengpo/internal/domain/observation"
)

var ErrObservationConflict = errors.New("idempotency key reused for a different observation")
var ErrObservationNotFound = errors.New("observation not found")

// ObservationRepository persists raw evidence only; it must not create memory nodes.
type ObservationRepository interface {
	StoreObservation(ctx context.Context, event observation.Event) (observation.Event, bool, error)
}

// ObservationReader retrieves one raw event by id within the caller's trusted
// tenant scope. Workers use it to normalize the exact event a job refers to.
type ObservationReader interface {
	GetObservation(ctx context.Context, tenantID, eventID string) (observation.Event, error)
}
