package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

const VectorDimensions = 384
const MaxVectorSearchResults = 100

var ErrInvalidVector = errors.New("invalid embedding vector or metadata")

type EmbeddingRepository struct {
	router *tenantdb.Router
}

func NewEmbeddingRepository(router *tenantdb.Router) *EmbeddingRepository {
	return &EmbeddingRepository{router: router}
}

func (r *EmbeddingRepository) SaveEmbedding(ctx context.Context, tenantID, memoryID, modelID, artifact, version string, vector []float32) error {
	literal, err := encodeVector(vector)
	if err != nil || memoryID == "" || modelID == "" || artifact == "" || version == "" {
		return ErrInvalidVector
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE memory_nodes
			SET embedding = $1::vector,
				embedding_model = $2,
				embedding_artifact = $3,
				embedding_version = $4,
				embedding_status = 'ready',
				updated_at = now()
			WHERE id = $5 AND deleted_at IS NULL`,
			literal, modelID, artifact, version, memoryID)
		if err != nil {
			return fmt.Errorf("save memory embedding: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check embedding update: %w", err)
		}
		if rows != 1 {
			return ErrMemoryNotFound
		}
		return nil
	})
}

func (r *EmbeddingRepository) SearchSimilar(ctx context.Context, tenantID, userID, sessionID, modelID, artifact, version string, vector []float32, limit int) ([]ports.VectorSearchResult, error) {
	literal, err := encodeVector(vector)
	if err != nil || tenantID == "" || userID == "" || modelID == "" || artifact == "" || version == "" || limit < 1 || limit > MaxVectorSearchResults {
		return nil, ErrInvalidVector
	}
	results := make([]ports.VectorSearchResult, 0, limit)
	err = r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT `+memoryNodeColumns+`, embedding <=> $1::vector AS distance
			FROM memory_nodes
			WHERE user_id = $5::uuid
			  AND ((scope_type = 'user-global' AND scope_id = $5::uuid)
			    OR (scope_type = 'session' AND session_id = NULLIF($6, '')::uuid))
AND ($6 = '' OR EXISTS (SELECT 1 FROM sessions AS requested_session WHERE requested_session.id = NULLIF($6, '')::uuid AND requested_session.user_id = $5::uuid))
			  AND status IN ('active', 'stable')
			  AND default_retrieval = true
			  AND deleted_at IS NULL
			  AND (expires_at IS NULL OR expires_at > now())
			  AND embedding_status = 'ready'
			  AND embedding IS NOT NULL
			  AND embedding_model = $2
			  AND embedding_artifact = $3
			  AND embedding_version = $4
			ORDER BY embedding <=> $1::vector, confidence DESC, updated_at DESC, id
			LIMIT $7`,
			literal, modelID, artifact, version, userID, sessionID, limit)
		if err != nil {
			return fmt.Errorf("query similar memory embeddings: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var result ports.VectorSearchResult
			result.Node, result.Distance, err = scanMemoryWithScore(rows)
			if err != nil {
				return fmt.Errorf("scan similar memory: %w", err)
			}
			results = append(results, result)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func encodeVector(vector []float32) (string, error) {
	if len(vector) != VectorDimensions {
		return "", ErrInvalidVector
	}
	var builder strings.Builder
	builder.Grow(2 + len(vector)*8)
	builder.WriteByte('[')
	normSquared := float64(0)
	for index, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", ErrInvalidVector
		}
		normSquared += float64(value) * float64(value)
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	if normSquared == 0 {
		return "", ErrInvalidVector
	}
	builder.WriteByte(']')
	return builder.String(), nil
}

const MaxEmbeddingJobClaimBatch = 100

func (r *EmbeddingRepository) EnqueueEmbeddingJob(ctx context.Context, tenantID, memoryID, modelID, artifact, version, idempotencyKey string) error {
	if memoryID == "" || modelID == "" || artifact == "" || version == "" || idempotencyKey == "" {
		return ErrInvalidVector
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (id, memory_id, model_id, artifact, model_version, idempotency_key)
			VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4, $5)
			ON CONFLICT (idempotency_key) DO UPDATE
			SET status = CASE WHEN embedding_jobs.status IN ('succeeded', 'failed', 'cancelled') THEN 'queued' ELSE embedding_jobs.status END,
			    attempts = CASE WHEN embedding_jobs.status IN ('succeeded', 'failed', 'cancelled') THEN 0 ELSE embedding_jobs.attempts END,
			    updated_at = now()
			WHERE embedding_jobs.memory_id = $1::uuid AND embedding_jobs.model_version = $4`,
			memoryID, modelID, artifact, version, idempotencyKey); err != nil {
			return fmt.Errorf("enqueue embedding job: %w", err)
		}
		return nil
	})
}

func (r *EmbeddingRepository) RebuildForModel(ctx context.Context, tenantID, modelID, artifact, version string) (int, error) {
	if tenantID == "" || modelID == "" || artifact == "" || version == "" {
		return 0, ErrInvalidVector
	}
	var enqueued int
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE memory_nodes
			SET embedding_status = 'stale', updated_at = now()
			WHERE deleted_at IS NULL AND embedding IS NOT NULL
			  AND (embedding_model, embedding_artifact, embedding_version)
			     IS DISTINCT FROM ($1, $2, $3)`,
			modelID, artifact, version); err != nil {
			return fmt.Errorf("mark stale memory embeddings: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			WITH pending AS (
				SELECT id FROM memory_nodes
				WHERE deleted_at IS NULL AND coalesce(content_text, '') <> ''
				  AND (embedding_model, embedding_artifact, embedding_version)
				     IS DISTINCT FROM ($1, $2, $3)
			), inserted AS (
				INSERT INTO embedding_jobs (id, memory_id, model_id, artifact, model_version, idempotency_key)
				SELECT gen_random_uuid(), pending.id, $1, $2, $3, 'embed:' || pending.id::text || ':' || $3
				FROM pending
				ON CONFLICT (idempotency_key) DO UPDATE
				SET status = CASE WHEN embedding_jobs.status IN ('succeeded', 'failed', 'cancelled') THEN 'queued' ELSE embedding_jobs.status END,
				    attempts = CASE WHEN embedding_jobs.status IN ('succeeded', 'failed', 'cancelled') THEN 0 ELSE embedding_jobs.attempts END,
				    updated_at = now()
			)
			SELECT count(*) FROM pending`,
			modelID, artifact, version).Scan(&enqueued); err != nil {
			return fmt.Errorf("enqueue rebuild embedding jobs: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return enqueued, nil
}

func (r *EmbeddingRepository) ClaimEmbeddingJobs(ctx context.Context, tenantID string, limit int) ([]ports.EmbeddingJob, error) {
	if limit < 1 || limit > MaxEmbeddingJobClaimBatch {
		return nil, ErrInvalidVector
	}
	jobs := make([]ports.EmbeddingJob, 0, limit)
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			WITH claimed AS (
				SELECT embedding_jobs.id
				FROM embedding_jobs
				WHERE status IN ('queued', 'retrying')
				ORDER BY created_at, id
				LIMIT $1
				FOR UPDATE SKIP LOCKED
			)
			UPDATE embedding_jobs AS job
			SET status = 'running', attempts = job.attempts + 1, updated_at = now()
			FROM claimed, memory_nodes AS node
			WHERE job.id = claimed.id AND job.memory_id = node.id AND node.deleted_at IS NULL
			RETURNING job.id, job.memory_id, job.model_id, job.artifact, job.model_version,
			          job.status, job.attempts, job.idempotency_key, job.last_error, node.content_text`,
			limit)
		if err != nil {
			return fmt.Errorf("claim embedding jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var job ports.EmbeddingJob
			var lastError sql.NullString
			if err := rows.Scan(&job.ID, &job.MemoryID, &job.ModelID, &job.Artifact, &job.ModelVersion,
				&job.Status, &job.Attempts, &job.IdempotencyKey, &lastError, &job.ContentText); err != nil {
				return fmt.Errorf("scan claimed embedding job: %w", err)
			}
			job.LastError = lastError.String
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *EmbeddingRepository) CompleteEmbeddingJob(ctx context.Context, tenantID, jobID string) error {
	return r.updateEmbeddingJob(ctx, tenantID, jobID, `
		UPDATE embedding_jobs
		SET status = 'succeeded', last_error = NULL, updated_at = now()
		WHERE id = $1::uuid AND status = 'running'`, nil)
}

func (r *EmbeddingRepository) RetryEmbeddingJob(ctx context.Context, tenantID, jobID, lastErr string, maxAttempts int) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return r.updateEmbeddingJob(ctx, tenantID, jobID, `
		UPDATE embedding_jobs
		SET status = CASE WHEN attempts >= $3 THEN 'failed' ELSE 'retrying' END,
		    last_error = $2,
		    updated_at = now()
		WHERE id = $1::uuid AND status = 'running'`,
		func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
				UPDATE memory_nodes AS node
				SET embedding_status = CASE
					WHEN (SELECT status FROM embedding_jobs WHERE id = $1::uuid) = 'failed' THEN 'failed'
					ELSE 'pending' END,
				    updated_at = now()
				WHERE node.id = (SELECT memory_id FROM embedding_jobs WHERE id = $1::uuid)`,
				jobID); err != nil {
				return fmt.Errorf("mark memory embedding retry state: %w", err)
			}
			return nil
		}, lastErr, maxAttempts)
}

func (r *EmbeddingRepository) updateEmbeddingJob(ctx context.Context, tenantID, jobID, statement string, after func(*sql.Tx) error, args ...any) error {
	if jobID == "" {
		return ErrInvalidVector
	}
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, statement, append([]any{jobID}, args...)...)
		if err != nil {
			return fmt.Errorf("update embedding job: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check embedding job update: %w", err)
		}
		if rows != 1 {
			return ErrMemoryNotFound
		}
		if after != nil {
			return after(tx)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
