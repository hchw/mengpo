package maintenance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

// GovernanceApplier applies a governance command. Expiry runs through
// governance (versioned mutation plus audit) so maintenance never bypasses it.
type GovernanceApplier interface {
	Apply(ctx context.Context, tenantID, memoryID string, command memory.GovernanceCommand) (memory.GovernanceMutation, error)
}

// ExpiryJob expires memories whose TTL has elapsed. Each candidate is expired
// through the governance service, so a bare update can never slip through.
type ExpiryJob struct {
	PlanName   string
	Reader     ports.DueMemoryReader
	Governance GovernanceApplier
	BatchSize  int
	NewID      func() (string, error)
	Clock      func() time.Time
}

// Name implements ports.ScheduledJob.
func (j *ExpiryJob) Name() string { return j.PlanName }

// Run expires the tenant's due memories. A tenant with nothing due is skipped.
func (j *ExpiryJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Governance == nil {
		return errors.New("expiry job is not configured")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	due, err := j.Reader.ListDueMemories(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	now := j.now()
	for _, node := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		auditID, err := j.newID()
		if err != nil {
			return err
		}
		requestID, err := j.newID()
		if err != nil {
			return err
		}
		if _, err := j.Governance.Apply(ctx, tenantID, node.ID, memory.GovernanceCommand{
			Action:          memory.GovernanceExpire,
			ExpectedVersion: node.Version,
			AuditID:         auditID,
			ActorType:       "system",
			ActorID:         j.PlanName,
			RequestID:       requestID,
			At:              now,
		}); err != nil {
			// A concurrent change (version conflict) is expected and safe to
			// skip: the next window will re-evaluate the memory.
			continue
		}
	}
	return nil
}

func (j *ExpiryJob) now() time.Time {
	if j.Clock != nil {
		return j.Clock().UTC()
	}
	return time.Now().UTC()
}

func (j *ExpiryJob) newID() (string, error) {
	if j.NewID != nil {
		return j.NewID()
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(b[:])
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}
