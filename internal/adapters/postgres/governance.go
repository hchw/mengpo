package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidGovernanceMutation = errors.New("invalid memory governance mutation")

// LookupGovernanceMutation returns the current committed result for a matching
// audit ID, enabling safe application-level retries without replaying a terminal
// state transition.
func (r *MemoryRepository) LookupGovernanceMutation(ctx context.Context, tenantID, auditID, resourceID, actorType, actorID, action, requestID string) (ports.MemoryGovernanceMutationResult, bool, error) {
	if auditID == "" || resourceID == "" || actorType == "" || actorID == "" || action == "" || requestID == "" {
		return ports.MemoryGovernanceMutationResult{}, false, ErrInvalidGovernanceMutation
	}
	var result ports.MemoryGovernanceMutationResult
	found := false
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var audit ports.MemoryGovernanceAuditRecord
		var resourceType string
		var changes []byte
		err := tx.QueryRowContext(ctx, `
			SELECT id, actor_type, actor_id, action, resource_type, resource_id, request_id, changes, created_at
			FROM audit_events WHERE id = $1`, auditID).Scan(
			&audit.ID, &audit.ActorType, &audit.ActorID, &audit.Action, &resourceType, &audit.ResourceID,
			&audit.RequestID, &changes, &audit.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lookup governance audit event: %w", err)
		}
		if resourceType != "memory" || audit.ResourceID != resourceID || audit.ActorType != actorType ||
			audit.ActorID != actorID || audit.Action != action || audit.RequestID != requestID {
			return ErrInvalidGovernanceMutation
		}
		audit.ResourceType = resourceType
		audit.Changes = append(json.RawMessage(nil), changes...)
		result.Audit = audit
		result.Memory, err = getMemory(ctx, tx, resourceID)
		if err != nil {
			return err
		}
		var auditChanges struct {
			RelatedResourceID string `json:"related_resource_id"`
		}
		if err := json.Unmarshal(changes, &auditChanges); err != nil {
			return ErrInvalidGovernanceMutation
		}
		if auditChanges.RelatedResourceID != "" {
			replacement, err := getMemory(ctx, tx, auditChanges.RelatedResourceID)
			if err != nil {
				return err
			}
			result.Replacement = &replacement
			var relation ports.MemoryRelationRecord
			if err := tx.QueryRowContext(ctx, `
				SELECT id, source_memory_id, target_memory_id, relation_type, confidence, created_at
				FROM memory_relations WHERE source_memory_id = $1 AND target_memory_id = $2 AND relation_type = 'supersedes'`,
				replacement.ID, result.Memory.ID).Scan(&relation.ID, &relation.SourceMemoryID, &relation.TargetMemoryID,
				&relation.RelationType, &relation.Confidence, &relation.CreatedAt); err != nil {
				return fmt.Errorf("lookup governance replacement relation: %w", err)
			}
			result.Relation = &relation
		}
		found = true
		return nil
	})
	if err != nil {
		return ports.MemoryGovernanceMutationResult{}, false, err
	}
	return result, found, nil
}

// ApplyGovernanceMutation writes the versioned memory mutation, optional
// replacement/relation, derived-data invalidation, and audit event atomically
// within the tenant schema.
func (r *MemoryRepository) ApplyGovernanceMutation(ctx context.Context, tenantID string, mutation ports.MemoryGovernanceMutationRecord) (ports.MemoryGovernanceMutationResult, error) {
	if err := validateGovernanceMutationRecord(mutation); err != nil {
		return ports.MemoryGovernanceMutationResult{}, err
	}
	var result ports.MemoryGovernanceMutationResult
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		// Reusing an audit ID is the mutation idempotency key. Verify its
		// identity before returning the already committed result.
		var resourceID, action, actorType, actorID, requestID string
		var changes []byte
		err := tx.QueryRowContext(ctx, `
			SELECT resource_id, action, actor_type, actor_id, request_id, changes
			FROM audit_events WHERE id = $1`, mutation.Audit.ID).Scan(&resourceID, &action, &actorType, &actorID, &requestID, &changes)
		if err == nil {
			if resourceID != mutation.Audit.ResourceID || action != mutation.Audit.Action || actorType != mutation.Audit.ActorType || actorID != mutation.Audit.ActorID || requestID != mutation.Audit.RequestID {
				return ErrInvalidGovernanceMutation
			}
			var auditChanges struct {
				BeforeVersion          int64  `json:"before_version"`
				AfterVersion           int64  `json:"after_version"`
				RelatedResourceID      string `json:"related_resource_id"`
				RelatedResourceVersion int64  `json:"related_resource_version"`
			}
			if json.Unmarshal(changes, &auditChanges) != nil || auditChanges.BeforeVersion != mutation.ExpectedVersion ||
				auditChanges.AfterVersion != mutation.Memory.Version || relatedResourceID(mutation) != auditChanges.RelatedResourceID ||
				(mutation.Replacement != nil && auditChanges.RelatedResourceVersion != mutation.Replacement.Version) {
				return ErrInvalidGovernanceMutation
			}
			result.Memory, err = getMemory(ctx, tx, mutation.Memory.ID)
			if err != nil {
				return err
			}
			if mutation.Replacement != nil {
				replacement, err := getMemory(ctx, tx, mutation.Replacement.ID)
				if err != nil {
					return err
				}
				result.Replacement = &replacement
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check governance audit idempotency: %w", err)
		}

		updated, err := scanMemory(tx.QueryRowContext(ctx, `
			UPDATE memory_nodes
			SET parent_id = $1, status = $2, confidence = $3, applicability = $4::jsonb,
				content = $5::jsonb, content_text = $6, default_retrieval = $7,
				provenance = $8::jsonb, expires_at = $9, deleted_at = $10,
				embedding = CASE WHEN $11 THEN NULL ELSE embedding END,
				embedding_status = CASE WHEN $11 THEN 'stale' ELSE embedding_status END,
				version = version + 1, updated_at = $12
			WHERE id = $13 AND version = $14
			RETURNING `+memoryNodeColumns,
			nullable(mutation.Memory.ParentID), mutation.Memory.Status, mutation.Memory.Confidence,
			string(mutation.Memory.Applicability), string(mutation.Memory.Content), mutation.Memory.ContentText,
			mutation.Memory.DefaultRetrieval, string(mutation.Memory.Provenance), mutation.Memory.ExpiresAt,
			mutation.Memory.DeletedAt, mutation.InvalidateEmbedding, mutation.Memory.UpdatedAt,
			mutation.Memory.ID, mutation.ExpectedVersion))
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("persist governance memory version: %w", err)
			}
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM memory_nodes WHERE id = $1)`, mutation.Memory.ID).Scan(&exists); err != nil {
				return fmt.Errorf("check memory after governance version mismatch: %w", err)
			}
			if !exists {
				return ErrMemoryNotFound
			}
			return ErrVersionConflict
		}
		result.Memory = updated

		if mutation.InvalidateEmbedding {
			if err := invalidateMemoryProjectionData(ctx, tx, mutation.Memory); err != nil {
				return err
			}
		}
		if mutation.PurgeDerivedData {
			if err := purgeDeletedMemoryDerivedData(ctx, tx, mutation.Memory); err != nil {
				return err
			}
		}
		if mutation.Replacement != nil {
			replacement, err := insertGovernanceReplacement(ctx, tx, *mutation.Replacement)
			if err != nil {
				return err
			}
			result.Replacement = &replacement
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO memory_relations (id, source_memory_id, target_memory_id, relation_type, confidence, created_at)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				mutation.Relation.ID, mutation.Relation.SourceMemoryID, mutation.Relation.TargetMemoryID,
				mutation.Relation.RelationType, mutation.Relation.Confidence, mutation.Relation.CreatedAt); err != nil {
				return fmt.Errorf("insert governance replacement relation: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (id, actor_type, actor_id, action, resource_type, resource_id, request_id, changes, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`,
			mutation.Audit.ID, mutation.Audit.ActorType, mutation.Audit.ActorID, mutation.Audit.Action,
			mutation.Audit.ResourceType, mutation.Audit.ResourceID, mutation.Audit.RequestID,
			string(mutation.Audit.Changes), mutation.Audit.CreatedAt); err != nil {
			return fmt.Errorf("insert governance audit event: %w", err)
		}
		return nil
	})
	if err != nil {
		return ports.MemoryGovernanceMutationResult{}, err
	}
	return result, nil
}

func validateGovernanceMutationRecord(mutation ports.MemoryGovernanceMutationRecord) error {
	memoryRecord, audit := mutation.Memory, mutation.Audit
	if mutation.ExpectedVersion <= 0 || memoryRecord.ID == "" || memoryRecord.Version != mutation.ExpectedVersion+1 ||
		memoryRecord.UpdatedAt.IsZero() || audit.ID == "" || audit.ActorID == "" || audit.RequestID == "" ||
		audit.ResourceType != "memory" || audit.ResourceID != memoryRecord.ID || audit.Action != mutation.Action ||
		audit.CreatedAt.IsZero() || !json.Valid(audit.Changes) || !json.Valid(memoryRecord.Content) ||
		!json.Valid(memoryRecord.Applicability) || !json.Valid(memoryRecord.Provenance) {
		return ErrInvalidGovernanceMutation
	}
	if audit.ActorType != "user" && audit.ActorType != "agent" && audit.ActorType != "system" && audit.ActorType != "admin" {
		return ErrInvalidGovernanceMutation
	}
	var changes map[string]json.RawMessage
	if json.Unmarshal(audit.Changes, &changes) != nil {
		return ErrInvalidGovernanceMutation
	}
	allowedChanges := map[string]bool{
		"before_version": true, "after_version": true, "related_resource_id": true,
		"related_resource_version": true, "redacted": true, "derived_data_purged": true,
	}
	for key := range changes {
		if !allowedChanges[key] {
			return ErrInvalidGovernanceMutation
		}
	}
	var beforeVersion, afterVersion int64
	if json.Unmarshal(changes["before_version"], &beforeVersion) != nil ||
		json.Unmarshal(changes["after_version"], &afterVersion) != nil ||
		beforeVersion != mutation.ExpectedVersion || afterVersion != memoryRecord.Version {
		return ErrInvalidGovernanceMutation
	}
	var relatedID string
	if raw := changes["related_resource_id"]; len(raw) > 0 && json.Unmarshal(raw, &relatedID) != nil {
		return ErrInvalidGovernanceMutation
	}
	var relatedVersion int64
	if raw := changes["related_resource_version"]; len(raw) > 0 && json.Unmarshal(raw, &relatedVersion) != nil {
		return ErrInvalidGovernanceMutation
	}
	var redacted, derivedDataPurged bool
	if raw := changes["redacted"]; len(raw) > 0 && json.Unmarshal(raw, &redacted) != nil {
		return ErrInvalidGovernanceMutation
	}
	if raw := changes["derived_data_purged"]; len(raw) > 0 && json.Unmarshal(raw, &derivedDataPurged) != nil {
		return ErrInvalidGovernanceMutation
	}
	switch mutation.Action {
	case "reject":
		if memoryRecord.Status != "rejected" || memoryRecord.DefaultRetrieval || mutation.Replacement != nil || mutation.Relation != nil ||
			mutation.PurgeDerivedData || mutation.InvalidateEmbedding || redacted || derivedDataPurged || relatedID != "" {
			return ErrInvalidGovernanceMutation
		}
	case "expire":
		if memoryRecord.Status != "expired" || memoryRecord.DefaultRetrieval || memoryRecord.ExpiresAt == nil || mutation.Replacement != nil || mutation.Relation != nil ||
			mutation.PurgeDerivedData || mutation.InvalidateEmbedding || redacted || derivedDataPurged || relatedID != "" {
			return ErrInvalidGovernanceMutation
		}
	case "redact":
		if memoryRecord.DeletedAt != nil || mutation.Replacement != nil || mutation.Relation != nil || mutation.PurgeDerivedData ||
			!mutation.InvalidateEmbedding || !redacted || derivedDataPurged || relatedID != "" {
			return ErrInvalidGovernanceMutation
		}
	case "delete":
		if (memoryRecord.Status != "expired" && memoryRecord.Status != "rejected") || memoryRecord.DefaultRetrieval ||
			!mutation.PurgeDerivedData || !mutation.InvalidateEmbedding || memoryRecord.DeletedAt == nil ||
			memoryRecord.ContentText != "" || string(memoryRecord.Content) != `{}` || !derivedDataPurged || redacted || relatedID != "" ||
			mutation.Replacement != nil || mutation.Relation != nil {
			return ErrInvalidGovernanceMutation
		}
	case "correct", "supersede":
		if memoryRecord.Status != "expired" || memoryRecord.DefaultRetrieval || memoryRecord.ExpiresAt == nil ||
			mutation.Replacement == nil || mutation.Relation == nil || mutation.PurgeDerivedData || mutation.InvalidateEmbedding ||
			mutation.Relation.ID == "" || mutation.Relation.RelationType != "supersedes" ||
			mutation.Relation.SourceMemoryID != mutation.Replacement.ID || mutation.Relation.TargetMemoryID != memoryRecord.ID ||
			redacted || derivedDataPurged || relatedID != mutation.Replacement.ID || relatedVersion != mutation.Replacement.Version || mutation.Relation.CreatedAt.IsZero() {
			return ErrInvalidGovernanceMutation
		}
	default:
		return ErrInvalidGovernanceMutation
	}
	if mutation.Replacement != nil {
		replacement := mutation.Replacement
		if replacement.ID == "" || replacement.IdempotencyKey == "" || replacement.Version != 1 || replacement.UserID != memoryRecord.UserID ||
			replacement.ScopeType != memoryRecord.ScopeType || replacement.ScopeID != memoryRecord.ScopeID ||
			replacement.SessionID != memoryRecord.SessionID || replacement.ID == memoryRecord.ID ||
			!json.Valid(replacement.Content) || !json.Valid(replacement.Applicability) || !json.Valid(replacement.Provenance) ||
			replacement.Status != "candidate" && replacement.Status != "active" && replacement.Status != "stable" {
			return ErrInvalidGovernanceMutation
		}
		if mutation.Action == "correct" && (replacement.Status != "stable" || replacement.Confidence != 1 || !replacement.DefaultRetrieval) {
			return ErrInvalidGovernanceMutation
		}
	}
	return nil
}

func relatedResourceID(mutation ports.MemoryGovernanceMutationRecord) string {
	if mutation.Replacement == nil {
		return ""
	}
	return mutation.Replacement.ID
}

func insertGovernanceReplacement(ctx context.Context, tx *sql.Tx, node ports.MemoryNodeRecord) (ports.MemoryNodeRecord, error) {
	if node.IdempotencyKey == "" || node.UserID == "" || node.ScopeID == "" || node.ScopeType == "" ||
		node.MemoryType == "" || node.Status == "" || !json.Valid(node.Content) || !json.Valid(node.Applicability) || !json.Valid(node.Provenance) {
		return ports.MemoryNodeRecord{}, ErrInvalidGovernanceMutation
	}
	if node.Visibility == "" {
		node.Visibility = "private"
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now().UTC()
	}
	if node.UpdatedAt.IsZero() {
		node.UpdatedAt = node.CreatedAt
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO memory_nodes (
			id, idempotency_key, user_id, session_id, scope_type, scope_id, parent_id,
			memory_type, status, visibility, confidence, applicability, content,
			content_text, default_retrieval, version, provenance, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb,
			$14, $15, 1, $16::jsonb, $17, $18
		)`,
		node.ID, node.IdempotencyKey, node.UserID, nullable(node.SessionID), node.ScopeType, node.ScopeID,
		nullable(node.ParentID), node.MemoryType, node.Status, node.Visibility, node.Confidence,
		string(node.Applicability), string(node.Content), node.ContentText, node.DefaultRetrieval,
		string(node.Provenance), node.CreatedAt, node.UpdatedAt)
	if err != nil {
		return ports.MemoryNodeRecord{}, fmt.Errorf("insert governance replacement memory: %w", err)
	}
	result, err := getMemory(ctx, tx, node.ID)
	if err != nil {
		return ports.MemoryNodeRecord{}, err
	}
	return result, nil
}

func invalidateMemoryProjectionData(ctx context.Context, tx *sql.Tx, memory ports.MemoryNodeRecord) error {
	statements := []struct {
		label string
		sql   string
		arg   string
	}{
		{"delete retrieval cache", `DELETE FROM retrieval_cache WHERE user_id = $1`, memory.UserID},
		{"delete retrieval traces", `DELETE FROM retrieval_events WHERE $1::uuid = ANY(selected_memory_ids)`, memory.ID},
		{"delete projection traces", `DELETE FROM projection_events WHERE $1::uuid = ANY(selected_memory_ids)`, memory.ID},
		{"cancel stale embedding jobs", `UPDATE embedding_jobs SET status = 'cancelled', last_error = 'memory_content_changed', updated_at = now() WHERE memory_id = $1 AND status IN ('queued', 'running', 'retrying')`, memory.ID},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.sql, statement.arg); err != nil {
			return fmt.Errorf("%s: %w", statement.label, err)
		}
	}
	return nil
}

func purgeDeletedMemoryDerivedData(ctx context.Context, tx *sql.Tx, memory ports.MemoryNodeRecord) error {
	// Raw observed_events and normalized_events are deliberately retained under
	// their independent retention policy; only the memory-specific derivations
	// and links are removed here.
	statements := []struct {
		label string
		sql   string
	}{
		{"delete evidence links", `DELETE FROM memory_evidence WHERE memory_id = $1`},
		{"delete memory relations", `DELETE FROM memory_relations WHERE source_memory_id = $1 OR target_memory_id = $1`},
		{"delete memory feedback", `DELETE FROM memory_feedback WHERE memory_id = $1`},
		{"delete embedding jobs", `DELETE FROM embedding_jobs WHERE memory_id = $1`},
		{"clear analysis job results", `UPDATE analysis_jobs SET status = 'cancelled', result = NULL, last_error = 'memory_deleted', updated_at = now() WHERE outbox_job_id IN (SELECT id FROM outbox_jobs WHERE aggregate_id = $1 OR position($1::text in payload::text) > 0)`},
		{"cancel memory outbox jobs", `UPDATE outbox_jobs SET status = 'cancelled', payload = '{}'::jsonb, lease_owner = NULL, lease_until = NULL, last_error = 'memory_deleted', updated_at = now() WHERE (aggregate_id = $1 OR position($1::text in payload::text) > 0) AND status IN ('queued', 'leased', 'running', 'retrying')`},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.sql, memory.ID); err != nil {
			return fmt.Errorf("%s: %w", statement.label, err)
		}
	}
	return nil
}
