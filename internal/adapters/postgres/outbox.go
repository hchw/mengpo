package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

var ErrJobLeaseLost = errors.New("outbox job lease lost")

type OutboxRepository struct{ router *tenantdb.Router }

func NewOutboxRepository(router *tenantdb.Router) *OutboxRepository {
	return &OutboxRepository{router: router}
}

// EnqueueJob inserts one durable job. The idempotency key makes enqueueing safe
// to retry; a duplicate key is silently ignored so callers never create double
// work. It writes only a tenant-scoped row, never a broker message.
func (r *OutboxRepository) EnqueueJob(ctx context.Context, job ports.OutboxJob) error {
	if job.ID == "" || job.TenantID == "" || job.JobType == "" || job.IdempotencyKey == "" {
		return errors.New("outbox job requires id, tenant, type and idempotency key")
	}
	payload := job.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return errors.New("outbox job payload must be valid JSON")
	}
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	var aggregateID any
	if job.AggregateID != "" {
		aggregateID = job.AggregateID
	}
	return r.router.WithTenantTx(ctx, job.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO outbox_jobs (id, tenant_id, job_type, aggregate_id, idempotency_key, payload, max_attempts)
			VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6::jsonb, $7)
			ON CONFLICT (idempotency_key) DO NOTHING`, job.ID, job.TenantID, job.JobType, aggregateID, job.IdempotencyKey, string(payload), maxAttempts)
		if err != nil {
			return fmt.Errorf("enqueue outbox job: %w", err)
		}
		return nil
	})
}

func (r *OutboxRepository) LeaseJobs(ctx context.Context, tenantID, workerID string, limit int, lease time.Duration) ([]ports.OutboxJob, error) {
	if tenantID == "" || workerID == "" || limit < 1 || limit > 100 || lease <= 0 {
		return nil, errors.New("invalid outbox lease request")
	}
	jobs := make([]ports.OutboxJob, 0, limit)
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE outbox_jobs SET status='dead_letter', lease_owner=NULL, lease_until=NULL, last_error=COALESCE(last_error, 'lease_expired_after_max_attempts'), updated_at=now() WHERE tenant_id=$1::uuid AND attempts >= max_attempts AND status IN ('leased','running') AND lease_until <= now()`, tenantID); err != nil {
			return fmt.Errorf("dead-letter exhausted expired jobs: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `WITH candidates AS (
			SELECT id FROM outbox_jobs
			WHERE tenant_id = $1::uuid AND attempts < max_attempts AND (
				(status IN ('queued', 'retrying') AND available_at <= now()) OR
				(status IN ('leased', 'running') AND lease_until <= now())
			)
			ORDER BY priority DESC, available_at, created_at, id
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE outbox_jobs AS job
		SET status = 'leased', lease_owner = $3, lease_until = now() + $4::interval,
			attempts = job.attempts + 1, updated_at = now()
		FROM candidates WHERE job.id = candidates.id
		RETURNING job.id::text, job.tenant_id::text, job.job_type, COALESCE(job.aggregate_id::text, ''),
			job.idempotency_key, job.payload, job.status, job.attempts, job.max_attempts, job.lease_owner, job.lease_until`,
			tenantID, limit, workerID, lease.String())
		if err != nil {
			return fmt.Errorf("lease outbox jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var job ports.OutboxJob
			if err := rows.Scan(&job.ID, &job.TenantID, &job.JobType, &job.AggregateID, &job.IdempotencyKey,
				&job.Payload, &job.Status, &job.Attempts, &job.MaxAttempts, &job.LeaseOwner, &job.LeaseUntil); err != nil {
				return fmt.Errorf("scan leased outbox job: %w", err)
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *OutboxRepository) StartJob(ctx context.Context, tenantID, jobID, workerID string) error {
	return r.updateOwnedJob(ctx, tenantID, jobID, workerID, `UPDATE outbox_jobs SET status='running', updated_at=now() WHERE id=$1::uuid AND lease_owner=$2 AND lease_until > now() AND status='leased'`)
}

func (r *OutboxRepository) CompleteJob(ctx context.Context, tenantID, jobID, workerID string) error {
	return r.updateOwnedJob(ctx, tenantID, jobID, workerID, `UPDATE outbox_jobs SET status='succeeded', lease_owner=NULL, lease_until=NULL, updated_at=now() WHERE id=$1::uuid AND lease_owner=$2 AND lease_until > now() AND status IN ('leased','running')`)
}

func (r *OutboxRepository) RetryJob(ctx context.Context, tenantID, jobID, workerID, failure string, retryAt time.Time) error {
	return r.updateOwnedJob(ctx, tenantID, jobID, workerID, `UPDATE outbox_jobs SET status=CASE WHEN attempts >= max_attempts THEN 'dead_letter' ELSE 'retrying' END, last_error=$3, available_at=$4, lease_owner=NULL, lease_until=NULL, updated_at=now() WHERE id=$1::uuid AND lease_owner=$2 AND lease_until > now() AND status IN ('leased','running')`, failure, retryAt)
}

// ReplayDeadLetter requeues a dead-lettered job without changing its idempotency key or payload.
// It remains subject to the original retry limit; operators must explicitly repair/reset policy elsewhere.
func (r *OutboxRepository) ReplayDeadLetter(ctx context.Context, tenantID, jobID string) error {
	if jobID == "" {
		return errors.New("job id is required")
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE outbox_jobs SET status='queued', attempts=0, available_at=now(), lease_owner=NULL, lease_until=NULL, last_error=NULL, updated_at=now() WHERE id=$1::uuid AND status='dead_letter'`, jobID)
		if err != nil {
			return fmt.Errorf("replay dead-letter job: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrJobLeaseLost
		}
		return nil
	})
}

func (r *OutboxRepository) CancelJob(ctx context.Context, tenantID, jobID string) error {
	if jobID == "" {
		return errors.New("job id is required")
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE outbox_jobs SET status='cancelled', lease_owner=NULL, lease_until=NULL, updated_at=now() WHERE id=$1::uuid AND status NOT IN ('succeeded','dead_letter','cancelled')`, jobID)
		if err != nil {
			return fmt.Errorf("cancel outbox job: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrJobLeaseLost
		}
		return nil
	})
}

func (r *OutboxRepository) updateOwnedJob(ctx context.Context, tenantID, jobID, workerID, query string, extra ...any) error {
	if jobID == "" || workerID == "" {
		return errors.New("job id and worker id are required")
	}
	args := []any{jobID, workerID}
	args = append(args, extra...)
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("update outbox job: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrJobLeaseLost
		}
		return nil
	})
}

// PurgeDeadLetterJobs deletes dead-letter jobs created before olderThan and
// reports how many were removed. Cancel/complete states are left untouched.
func (r *OutboxRepository) PurgeDeadLetterJobs(ctx context.Context, tenantID string, olderThan time.Time) (int, error) {
	if tenantID == "" {
		return 0, nil
	}
	removed := 0
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM outbox_jobs WHERE status = 'dead_letter' AND created_at < $1`, olderThan.UTC())
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		removed = int(rows)
		return nil
	})
	return removed, err
}

var _ ports.OutboxMaintenanceRepository = (*OutboxRepository)(nil)
