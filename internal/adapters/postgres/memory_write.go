package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hchw/mengpo/internal/ports"
)

// validateMemoryWriteNode applies the shared shape checks every writer needs
// before touching the database. It deliberately knows nothing about scope or
// status; each writer owns its own policy on top of it.
func validateMemoryWriteNode(node ports.MemoryNodeRecord) error {
	if node.ID == "" || node.IdempotencyKey == "" || node.UserID == "" || node.ScopeID == "" ||
		node.ScopeType == "" || !json.Valid(node.Content) {
		return ErrInvalidMemory
	}
	return nil
}

// insertMemoryNodeTx inserts a memory node inside an open tenant transaction.
// It returns the effective memory id — which may belong to a pre-existing row
// with the same idempotency key — and whether a new row was created.
func insertMemoryNodeTx(ctx context.Context, tx *sql.Tx, node ports.MemoryNodeRecord) (string, bool, error) {
	if node.Visibility == "" {
		node.Visibility = "private"
	}
	if len(node.Applicability) == 0 {
		node.Applicability = json.RawMessage(`{}`)
	}
	if len(node.Provenance) == 0 {
		node.Provenance = json.RawMessage(`{}`)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO memory_nodes (
	id, idempotency_key, user_id, session_id, scope_type, scope_id, parent_id,
	memory_type, status, visibility, confidence, applicability, content,
	content_text, default_retrieval, version, created_at, updated_at, provenance
) VALUES (
	$1, $2, $3, $4, $5, $6, NULL, $7, $8, $9, $10, $11::jsonb, $12::jsonb,
	$13, $14, 1, now(), now(), $15::jsonb
) ON CONFLICT (idempotency_key) DO NOTHING`,
		node.ID, node.IdempotencyKey, node.UserID, nullable(node.SessionID), node.ScopeType,
		node.ScopeID, node.MemoryType, node.Status, node.Visibility, node.Confidence,
		string(node.Applicability), string(node.Content), node.ContentText, node.DefaultRetrieval,
		string(node.Provenance))
	if err != nil {
		return "", false, fmt.Errorf("insert memory node: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("check memory insert: %w", err)
	}
	if rows > 0 {
		return node.ID, true, nil
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM memory_nodes WHERE idempotency_key = $1`, node.IdempotencyKey).Scan(&existing); err != nil {
		return "", false, fmt.Errorf("resolve existing memory: %w", err)
	}
	return existing, false, nil
}

// insertMemoryEvidenceTx writes the evidence rows for one memory node. A
// missing attribution defaults to inferred so an unreviewed claim is never
// mistaken for a directly verified one.
func insertMemoryEvidenceTx(ctx context.Context, tx *sql.Tx, memoryID string, evidence []ports.MemoryEvidenceRecord) error {
	for _, item := range evidence {
		if item.RawEventID == "" || item.EvidenceRole == "" {
			return fmt.Errorf("%w: evidence requires raw event and role", ErrInvalidMemory)
		}
		attribution := item.Attribution
		if attribution == "" {
			attribution = "inferred"
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_evidence (id, memory_id, raw_event_id, normalized_event_id, evidence_role, confidence, attribution)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
ON CONFLICT (memory_id, raw_event_id, evidence_role) DO NOTHING`,
			memoryID, item.RawEventID, nullable(item.NormalizedEventID), item.EvidenceRole,
			item.Confidence, attribution); err != nil {
			return fmt.Errorf("insert memory evidence: %w", err)
		}
	}
	return nil
}
