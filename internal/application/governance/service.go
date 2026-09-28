package governance

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

var (
	ErrRepositoryRequired  = errors.New("governance repository is required")
	ErrInvalidMemoryRecord = errors.New("invalid persisted memory record")
	ErrPersistenceMismatch = errors.New("persisted governance mutation does not match domain result")
)

// Repository combines ordinary memory reads with the atomic governance write
// port. Authorization of the tenant and requested memory Scope is performed by
// the calling application boundary before invoking this service.
type Repository interface {
	ports.MemoryNodeRepository
	ports.MemoryGovernanceMutationRepository
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Apply(ctx context.Context, tenantID, memoryID string, command memory.GovernanceCommand) (memory.GovernanceMutation, error) {
	if s == nil || s.repository == nil {
		return memory.GovernanceMutation{}, ErrRepositoryRequired
	}
	if tenantID == "" || memoryID == "" {
		return memory.GovernanceMutation{}, ErrInvalidMemoryRecord
	}
	replayed, found, err := s.repository.LookupGovernanceMutation(ctx, tenantID, command.AuditID, memoryID, command.ActorType, command.ActorID, string(command.Action), command.RequestID)
	if err != nil {
		return memory.GovernanceMutation{}, err
	}
	if found {
		return fromPersistedMutation(replayed, command.Action, command.ExpectedVersion)
	}
	record, err := s.repository.Get(ctx, tenantID, memoryID)
	if err != nil {
		return memory.GovernanceMutation{}, err
	}
	current, err := FromRecord(record)
	if err != nil {
		return memory.GovernanceMutation{}, err
	}
	mutation, err := memory.ApplyGovernanceAction(current, command)
	if err != nil {
		return memory.GovernanceMutation{}, err
	}
	persisted, err := s.repository.ApplyGovernanceMutation(ctx, tenantID, ToRecord(mutation, command.ExpectedVersion))
	if err != nil {
		return memory.GovernanceMutation{}, err
	}
	if persisted.Memory.ID != mutation.Memory.ID || persisted.Memory.Version != mutation.Memory.Version || persisted.Memory.Status != string(mutation.Memory.Status) {
		return memory.GovernanceMutation{}, ErrPersistenceMismatch
	}
	if mutation.Replacement != nil && (persisted.Replacement == nil || persisted.Replacement.ID != mutation.Replacement.ID || persisted.Replacement.Version != mutation.Replacement.Version) {
		return memory.GovernanceMutation{}, ErrPersistenceMismatch
	}
	return mutation, nil
}

func fromPersistedMutation(result ports.MemoryGovernanceMutationResult, action memory.GovernanceAction, expectedVersion int64) (memory.GovernanceMutation, error) {
	var auditChanges memory.GovernanceAuditChanges
	if json.Unmarshal(result.Audit.Changes, &auditChanges) != nil || auditChanges.BeforeVersion != expectedVersion {
		return memory.GovernanceMutation{}, memory.ErrGovernanceVersionConflict
	}
	current, err := FromRecord(result.Memory)
	if err != nil {
		return memory.GovernanceMutation{}, err
	}
	mutation := memory.GovernanceMutation{
		Action: action, Memory: current,
		PurgeDerivedData:    action == memory.GovernanceDelete,
		InvalidateEmbedding: action == memory.GovernanceDelete || action == memory.GovernanceRedact,
		Audit: memory.GovernanceAuditEvent{
			ID: result.Audit.ID, ActorType: result.Audit.ActorType, ActorID: result.Audit.ActorID,
			Action: result.Audit.Action, ResourceType: result.Audit.ResourceType, ResourceID: result.Audit.ResourceID,
			RequestID: result.Audit.RequestID, Changes: append(json.RawMessage(nil), result.Audit.Changes...),
			CreatedAt: result.Audit.CreatedAt,
		},
	}
	if result.Replacement != nil {
		replacement, err := FromRecord(*result.Replacement)
		if err != nil {
			return memory.GovernanceMutation{}, err
		}
		mutation.Replacement = &replacement
	}
	if result.Relation != nil {
		mutation.Relation = &memory.Relation{
			ID: result.Relation.ID, SourceMemoryID: result.Relation.SourceMemoryID,
			TargetMemoryID: result.Relation.TargetMemoryID, Type: memory.RelationType(result.Relation.RelationType),
			Confidence: result.Relation.Confidence, CreatedAt: result.Relation.CreatedAt,
		}
	}
	return mutation, nil
}

func FromRecord(record ports.MemoryNodeRecord) (memory.Memory, error) {
	var applicability memory.Applicability
	var provenance memory.Provenance
	if !json.Valid(record.Applicability) || json.Unmarshal(record.Applicability, &applicability) != nil ||
		!json.Valid(record.Provenance) || json.Unmarshal(record.Provenance, &provenance) != nil {
		return memory.Memory{}, ErrInvalidMemoryRecord
	}
	result := memory.Memory{
		ID: record.ID, IdempotencyKey: record.IdempotencyKey, UserID: record.UserID,
		SessionID: record.SessionID, ScopeType: memory.ScopeType(record.ScopeType), ScopeID: record.ScopeID,
		ParentID: record.ParentID, Type: record.MemoryType, Status: memory.MemoryStatus(record.Status),
		Visibility: memory.Visibility(record.Visibility), Confidence: record.Confidence,
		Applicability: applicability, Content: append(json.RawMessage(nil), record.Content...),
		ContentText: record.ContentText, DefaultRetrieval: record.DefaultRetrieval, Version: record.Version,
		Provenance: provenance, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		ExpiresAt: record.ExpiresAt, DeletedAt: record.DeletedAt,
	}
	if err := result.Validate(); err != nil {
		return memory.Memory{}, ErrInvalidMemoryRecord
	}
	return result, nil
}

func ToRecord(mutation memory.GovernanceMutation, expectedVersion int64) ports.MemoryGovernanceMutationRecord {
	toNodeRecord := func(node memory.Memory) ports.MemoryNodeRecord {
		applicability, _ := json.Marshal(node.Applicability)
		provenance, _ := json.Marshal(node.Provenance)
		return ports.MemoryNodeRecord{
			ID: node.ID, IdempotencyKey: node.IdempotencyKey, UserID: node.UserID,
			SessionID: node.SessionID, ScopeType: string(node.ScopeType), ScopeID: node.ScopeID,
			ParentID: node.ParentID, MemoryType: node.Type, Status: string(node.Status),
			Visibility: string(node.Visibility), Confidence: node.Confidence,
			Applicability: applicability, Content: append(json.RawMessage(nil), node.Content...),
			ContentText: node.ContentText, DefaultRetrieval: node.DefaultRetrieval, Version: node.Version,
			Provenance: provenance, CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt,
			ExpiresAt: node.ExpiresAt, DeletedAt: node.DeletedAt,
		}
	}
	record := ports.MemoryGovernanceMutationRecord{
		Action: string(mutation.Action), ExpectedVersion: expectedVersion, Memory: toNodeRecord(mutation.Memory),
		PurgeDerivedData: mutation.PurgeDerivedData, InvalidateEmbedding: mutation.InvalidateEmbedding,
		Audit: ports.MemoryGovernanceAuditRecord{
			ID: mutation.Audit.ID, ActorType: mutation.Audit.ActorType, ActorID: mutation.Audit.ActorID,
			Action: mutation.Audit.Action, ResourceType: mutation.Audit.ResourceType,
			ResourceID: mutation.Audit.ResourceID, RequestID: mutation.Audit.RequestID,
			Changes: append(json.RawMessage(nil), mutation.Audit.Changes...), CreatedAt: mutation.Audit.CreatedAt,
		},
	}
	if mutation.Replacement != nil {
		replacement := toNodeRecord(*mutation.Replacement)
		record.Replacement = &replacement
	}
	if mutation.Relation != nil {
		record.Relation = &ports.MemoryRelationRecord{
			ID: mutation.Relation.ID, SourceMemoryID: mutation.Relation.SourceMemoryID,
			TargetMemoryID: mutation.Relation.TargetMemoryID, RelationType: string(mutation.Relation.Type),
			Confidence: mutation.Relation.Confidence, CreatedAt: mutation.Relation.CreatedAt,
		}
	}
	return record
}
