package maintenance

import (
	"context"
	"errors"

	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

// contentTextStore reads nodes whose canonical projection is missing and stores
// a freshly rendered one.
type contentTextStore interface {
	ListMissingContentText(ctx context.Context, tenantID string, limit int) ([]ports.MemoryNodeRecord, error)
	RefreshContentText(ctx context.Context, tenantID, memoryID, contentText string) (bool, error)
}

// embeddingRebuilder re-enqueues embeddings for nodes whose projection changed.
// It reuses the model-identity rebuild, which only queues nodes whose embedding
// identity differs from the current one (a backfilled node has none yet).
type embeddingRebuilder interface {
	RebuildForModel(ctx context.Context, tenantID string) (int, error)
}

// ContentTextBackfillJob renders the canonical text projection for legacy nodes
// written before writers populated content_text. The renderer lives in Go, so a
// SQL migration cannot call it; repair is a scheduled job like the other
// maintenance plans. Nodes whose content truly holds no readable text keep an
// empty projection and are never treated as embeddable.
type ContentTextBackfillJob struct {
	PlanName   string
	Store      contentTextStore
	Embeddings embeddingRebuilder
	BatchSize  int
}

// Name implements ports.ScheduledJob.
func (j *ContentTextBackfillJob) Name() string { return j.PlanName }

// Run repairs one batch of nodes for the tenant. It is idempotent: the refresh
// only writes while the projection is still empty.
func (j *ContentTextBackfillJob) Run(ctx context.Context, tenantID string) error {
	if j.Store == nil {
		return errors.New("content text backfill job is not configured")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	nodes, err := j.Store.ListMissingContentText(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	updated := 0
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		text := memory.RenderContentText(node.Content)
		if text == "" {
			// No readable text: leave it empty rather than fabricating one.
			continue
		}
		changed, err := j.Store.RefreshContentText(ctx, tenantID, node.ID, text)
		if err != nil {
			return err
		}
		if changed {
			updated++
		}
	}
	if updated > 0 && j.Embeddings != nil {
		if _, err := j.Embeddings.RebuildForModel(ctx, tenantID); err != nil {
			return err
		}
	}
	return nil
}
