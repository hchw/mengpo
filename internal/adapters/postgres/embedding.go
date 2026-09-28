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
