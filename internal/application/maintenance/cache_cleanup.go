package maintenance

import (
	"context"

	"github.com/hchw/mengpo/internal/ports"
)

// CacheCleanupJob removes expired entries from the database-backed cache so it
// does not accumulate rows. It is a no-op when no cache is configured.
type CacheCleanupJob struct {
	PlanName  string
	Cache     ports.ExpiredCachePurger
	BatchSize int
}

// Name implements ports.ScheduledJob.
func (j *CacheCleanupJob) Name() string { return j.PlanName }

// Run purges one batch of expired cache entries for the tenant.
func (j *CacheCleanupJob) Run(ctx context.Context, tenantID string) error {
	if j.Cache == nil {
		return nil
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 500
	}
	_, err := j.Cache.PurgeExpired(ctx, tenantID, batchSize)
	return err
}
