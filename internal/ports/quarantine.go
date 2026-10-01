package ports

import (
	"context"
	"encoding/json"
	"time"
)

// QuarantinedEvent is a raw observation envelope that could not be bound to a
// trusted tenant. It is stored outside tenant schemas and is never normalized
// into memory.
type QuarantinedEvent struct {
	ID                    string
	ReceivedAt            time.Time
	Reason                string
	DeclaredTenant        string
	PrincipalType         string
	PrincipalID           string
	RequestID             string
	CredentialFingerprint string
	Payload               json.RawMessage
}

type QuarantineRepository interface {
	QuarantineEvent(ctx context.Context, event QuarantinedEvent) error
}
