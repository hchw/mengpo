package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// ErrInvalidSemanticNeighborQuery marks a near-neighbor query with a missing
// reference, a threshold outside (0,1], or a non-positive limit.
var ErrInvalidSemanticNeighborQuery = errors.New("invalid semantic neighbor query")

// lexicalSimilarityThreshold is the pg_trgm trigram similarity floor used when a
// memory has no usable embedding and the lexical fallback is enabled. It is
// deliberately looser than the embedding threshold because trigram overlap and
// cosine similarity are not comparable scales.
const lexicalSimilarityThreshold = 0.30

// SemanticNeighborRepository reads semantic near neighbors of a memory node.
type SemanticNeighborRepository struct {
	router *tenantdb.Router
}

// NewSemanticNeighborRepository builds the near-neighbor reader.
func NewSemanticNeighborRepository(router *tenantdb.Router) *SemanticNeighborRepository {
	return &SemanticNeighborRepository{router: router}
}

// EmbeddingReady reports whether the memory has a usable embedding for the
// current model identity. It lets callers record "cannot compare" separately
// from "not similar".
func (r *SemanticNeighborRepository) EmbeddingReady(ctx context.Context, tenantID, memoryID string) (bool, error) {
	if tenantID == "" || memoryID == "" {
		return false, ErrInvalidSemanticNeighborQuery
	}
	ready := false
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
SELECT embedding_status = 'ready' AND embedding IS NOT NULL
FROM memory_nodes
WHERE id = $1::uuid AND deleted_at IS NULL`, memoryID).Scan(&ready)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ready, nil
}

// ListSemanticNeighbors returns the reference memory's semantic neighbors in
// the same user and scope, using embeddings that share the reference's model
// identity. A reference without a ready embedding yields no semantic neighbors;
// with LexicalFallback it instead returns trigram-similar neighbors by text.
func (r *SemanticNeighborRepository) ListSemanticNeighbors(ctx context.Context, tenantID string, query ports.SemanticNeighborQuery) ([]ports.SemanticNeighbor, error) {
	if tenantID == "" || query.ReferenceMemoryID == "" || query.Threshold <= 0 || query.Threshold > 1 || query.Limit < 1 {
		return nil, ErrInvalidSemanticNeighborQuery
	}
	if query.Limit > MaxVectorSearchResults {
		query.Limit = MaxVectorSearchResults
	}
	var results []ports.SemanticNeighbor
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var (
			userID, scopeType, scopeID, sessionID string
			modelID, artifact, version            string
			contentText                           string
			hasEmbedding                          bool
		)
		err := tx.QueryRowContext(ctx, `
SELECT user_id::text, scope_type, scope_id::text, COALESCE(session_id::text, ''),
       COALESCE(embedding_model, ''), COALESCE(embedding_artifact, ''), COALESCE(embedding_version, ''),
       COALESCE(content_text, ''),
       (embedding_status = 'ready' AND embedding IS NOT NULL)
FROM memory_nodes
WHERE id = $1::uuid AND deleted_at IS NULL`, query.ReferenceMemoryID).
			Scan(&userID, &scopeType, &scopeID, &sessionID, &modelID, &artifact, &version, &contentText, &hasEmbedding)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load semantic reference: %w", err)
		}
		switch {
		case hasEmbedding:
			results, err = semanticNeighbors(ctx, tx, query, modelID, artifact, version)
		case query.LexicalFallback && contentText != "":
			results, err = lexicalNeighbors(ctx, tx, query, userID, scopeType, scopeID, sessionID, contentText)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func semanticNeighbors(ctx context.Context, tx *sql.Tx, query ports.SemanticNeighborQuery, modelID, artifact, version string) ([]ports.SemanticNeighbor, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT `+memoryNodeColumnList("n")+`, 1 - (n.embedding <=> ref.embedding) AS similarity
FROM memory_nodes AS n
JOIN memory_nodes AS ref ON ref.id = $1::uuid
WHERE n.id <> $1::uuid
  AND n.deleted_at IS NULL
  AND n.status <> 'rejected'
  AND n.embedding_status = 'ready'
  AND n.embedding IS NOT NULL
  AND n.embedding_model = $2
  AND n.embedding_artifact = $3
  AND n.embedding_version = $4
  AND n.user_id = ref.user_id
  AND n.scope_type = ref.scope_type
  AND (
    (ref.scope_type = 'session' AND n.session_id = ref.session_id)
    OR (ref.scope_type = 'user-global' AND n.scope_id = ref.scope_id)
  )
  AND 1 - (n.embedding <=> ref.embedding) >= $5
ORDER BY similarity DESC, n.confidence DESC, n.updated_at DESC, n.id
LIMIT $6`, query.ReferenceMemoryID, modelID, artifact, version, query.Threshold, query.Limit)
	if err != nil {
		return nil, fmt.Errorf("query semantic neighbors: %w", err)
	}
	return scanNeighbors(rows)
}

func lexicalNeighbors(ctx context.Context, tx *sql.Tx, query ports.SemanticNeighborQuery, userID, scopeType, scopeID, sessionID, contentText string) ([]ports.SemanticNeighbor, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT `+memoryNodeColumnList("n")+`, similarity(n.content_text, $1) AS similarity
FROM memory_nodes AS n
WHERE n.id <> $2::uuid
  AND n.deleted_at IS NULL
  AND n.status <> 'rejected'
  AND n.user_id = $3::uuid
  AND n.scope_type = $4
  AND (
    ($4 = 'session' AND n.session_id = NULLIF($5, '')::uuid)
    OR ($4 = 'user-global' AND n.scope_id = $6::uuid)
  )
  AND similarity(n.content_text, $1) >= $7
ORDER BY similarity DESC, n.confidence DESC, n.updated_at DESC, n.id
LIMIT $8`, contentText, query.ReferenceMemoryID, userID, scopeType, sessionID, scopeID, lexicalSimilarityThreshold, query.Limit)
	if err != nil {
		return nil, fmt.Errorf("query lexical neighbors: %w", err)
	}
	return scanNeighbors(rows)
}

func scanNeighbors(rows *sql.Rows) ([]ports.SemanticNeighbor, error) {
	defer rows.Close()
	var results []ports.SemanticNeighbor
	for rows.Next() {
		node, similarity, err := scanMemoryWithScore(rows)
		if err != nil {
			return nil, fmt.Errorf("scan semantic neighbor: %w", err)
		}
		results = append(results, ports.SemanticNeighbor{Node: node, Similarity: similarity})
	}
	return results, rows.Err()
}
