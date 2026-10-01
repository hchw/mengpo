package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

// MergeMemories unifies duplicate memories into a canonical target in one
// tenant transaction. It unions evidence, raises confidence, links each
// duplicate with a merged_into relation, retires the duplicates, and writes an
// audit event. Merge never deletes evidence.
func (r *MemoryRepository) MergeMemories(ctx context.Context, tenantID string, request ports.MergeRequest) (ports.MergeResult, error) {
	if tenantID == "" || request.TargetID == "" || len(request.DuplicateIDs) == 0 || request.ActorID == "" {
		return ports.MergeResult{}, ports.ErrInvalidMerge
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var result ports.MergeResult
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		target, err := lockMemory(ctx, tx, request.TargetID)
		if err != nil {
			return err
		}
		if target.DeletedAt != nil {
			return ports.ErrInvalidMerge
		}
		provenance := decodeProvenance(target.Provenance)
		seen := map[string]bool{target.ID: true}
		var duplicates []ports.MemoryNodeRecord
		for _, duplicateID := range request.DuplicateIDs {
			if duplicateID == "" || seen[duplicateID] {
				continue
			}
			seen[duplicateID] = true
			duplicate, err := lockMemory(ctx, tx, duplicateID)
			if err != nil {
				return err
			}
			if duplicate.DeletedAt != nil ||
				duplicate.UserID != target.UserID ||
				duplicate.ScopeType != target.ScopeType ||
				duplicate.ScopeID != target.ScopeID ||
				duplicate.SessionID != target.SessionID {
				return ports.ErrMergeScopeDrift
			}
			// Union evidence and de-duplicate source event ids.
			for _, id := range decodeProvenance(duplicate.Provenance).EvidenceEventIDs {
				if id != "" && !containsString(provenance.EvidenceEventIDs, id) {
					provenance.EvidenceEventIDs = append(provenance.EvidenceEventIDs, id)
				}
			}
			duplicates = append(duplicates, duplicate)
		}
		if len(duplicates) == 0 {
			return ports.ErrInvalidMerge
		}
		// Corroboration raises confidence but never past certainty.
		target.Confidence = math.Min(1, target.Confidence+0.1*float64(len(duplicates)))
		target.DefaultRetrieval = true
		if target.Status == "candidate" {
			// A merged candidate is corroborated by the duplicates' evidence.
			target.Status = "active"
		}
		target.Provenance = encodeProvenance(provenance)
		encodedProvenance, err := json.Marshal(provenance)
		if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE memory_nodes
			SET status = $5, provenance = $2::jsonb, confidence = $3, default_retrieval = true, version = version + 1, updated_at = now()
			WHERE id = $1::uuid AND version = $4 AND deleted_at IS NULL`,
			target.ID, string(encodedProvenance), target.Confidence, target.Version, target.Status)
		if err != nil {
			return fmt.Errorf("update merge target: %w", err)
		}
		if affected, _ := updated.RowsAffected(); affected == 0 {
			return ports.ErrInvalidMerge
		}
		for _, duplicate := range duplicates {
			relationID, err := newIDForMerge()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO memory_relations (id, source_memory_id, target_memory_id, relation_type, confidence, created_at)
				VALUES ($1::uuid, $2::uuid, $3::uuid, 'merged_into', 1, $4)`, relationID, duplicate.ID, target.ID, at); err != nil {
				return fmt.Errorf("insert merge relation: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE memory_nodes
				SET status = 'rejected', default_retrieval = false, version = version + 1, updated_at = now()
				WHERE id = $1::uuid AND deleted_at IS NULL`, duplicate.ID); err != nil {
				return fmt.Errorf("retire merged duplicate: %w", err)
			}
		}
		auditID, err := newIDForMerge()
		if err != nil {
			return err
		}
		changes, err := json.Marshal(map[string]any{
			"target_memory_id":  target.ID,
			"merged_memory_ids": duplicateIDs(duplicates),
			"evidence_union":    provenance.EvidenceEventIDs,
			"target_confidence": target.Confidence,
		})
		if err != nil {
			return err
		}
		actorType := request.ActorType
		if actorType == "" {
			actorType = "user"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, actor_type, actor_id, action, resource_type, resource_id, request_id, changes, created_at)
			VALUES ($1::uuid, $2, $3, 'merge', 'memory', $4, $5, $6::jsonb, $7)`,
			auditID, actorType, request.ActorID, target.ID, request.RequestID, string(changes), at); err != nil {
			return fmt.Errorf("write merge audit: %w", err)
		}
		target.Version++
		target.UpdatedAt = at
		result = ports.MergeResult{Target: target, Duplicates: duplicates}
		return nil
	})
	if err != nil {
		return ports.MergeResult{}, err
	}
	return result, nil
}

type mergeProvenance struct {
	EvidenceEventIDs []string `json:"evidence_event_ids"`
	RunID            string   `json:"run_id,omitempty"`
}

func decodeProvenance(raw json.RawMessage) mergeProvenance {
	var provenance mergeProvenance
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &provenance)
	}
	return provenance
}

func encodeProvenance(provenance mergeProvenance) json.RawMessage {
	encoded, err := json.Marshal(provenance)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func lockMemory(ctx context.Context, tx *sql.Tx, memoryID string) (ports.MemoryNodeRecord, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+memoryNodeColumnList("m")+` FROM memory_nodes AS m WHERE m.id = $1::uuid FOR UPDATE`, memoryID)
	node, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.MemoryNodeRecord{}, ports.ErrMemoryNotFound
	}
	if err != nil {
		return ports.MemoryNodeRecord{}, fmt.Errorf("lock memory: %w", err)
	}
	return node, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func duplicateIDs(nodes []ports.MemoryNodeRecord) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func newIDForMerge() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
