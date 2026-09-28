package memory

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidGovernanceCommand   = errors.New("invalid governance command")
	ErrGovernanceVersionConflict  = errors.New("governance memory version conflict")
	ErrGovernanceActionNotAllowed = errors.New("governance action not allowed for memory state")
)

type GovernanceAction string

const (
	GovernanceReject    GovernanceAction = "reject"
	GovernanceCorrect   GovernanceAction = "correct"
	GovernanceExpire    GovernanceAction = "expire"
	GovernanceDelete    GovernanceAction = "delete"
	GovernanceRedact    GovernanceAction = "redact"
	GovernanceSupersede GovernanceAction = "supersede"
)

type MemoryRedaction struct {
	Content       json.RawMessage
	ContentText   string
	Applicability Applicability
}

type GovernanceCommand struct {
	Action          GovernanceAction
	ExpectedVersion int64
	AuditID         string
	ActorType       string
	ActorID         string
	RequestID       string
	At              time.Time
	RelationID      string
	Replacement     *Memory
	Redaction       *MemoryRedaction
}

type GovernanceAuditChanges struct {
	BeforeVersion          int64  `json:"before_version"`
	AfterVersion           int64  `json:"after_version"`
	RelatedResourceID      string `json:"related_resource_id,omitempty"`
	RelatedResourceVersion int64  `json:"related_resource_version,omitempty"`
	Redacted               bool   `json:"redacted,omitempty"`
	DerivedDataPurged      bool   `json:"derived_data_purged,omitempty"`
}

type GovernanceAuditEvent struct {
	ID           string          `json:"id"`
	ActorType    string          `json:"actor_type"`
	ActorID      string          `json:"actor_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	RequestID    string          `json:"request_id"`
	Changes      json.RawMessage `json:"changes"`
	CreatedAt    time.Time       `json:"created_at"`
}

type GovernanceMutation struct {
	Action              GovernanceAction
	Memory              Memory
	Replacement         *Memory
	Relation            *Relation
	Audit               GovernanceAuditEvent
	PurgeDerivedData    bool
	InvalidateEmbedding bool
}

// ApplyGovernanceAction computes a versioned, auditable domain mutation. The
// caller must persist the returned node(s), relation, purge request, and audit
// event atomically through a tenant-scoped repository transaction.
func ApplyGovernanceAction(current Memory, command GovernanceCommand) (GovernanceMutation, error) {
	if err := validateGovernanceCommand(current, command); err != nil {
		return GovernanceMutation{}, err
	}

	updated := current
	updated.Version++
	updated.UpdatedAt = command.At.UTC()
	mutation := GovernanceMutation{Action: command.Action}
	changes := GovernanceAuditChanges{BeforeVersion: current.Version, AfterVersion: updated.Version}

	switch command.Action {
	case GovernanceReject:
		if err := updated.Transition(StatusRejected); err != nil {
			return GovernanceMutation{}, ErrGovernanceActionNotAllowed
		}
		updated.DefaultRetrieval = false

	case GovernanceExpire:
		if err := updated.Transition(StatusExpired); err != nil {
			return GovernanceMutation{}, ErrGovernanceActionNotAllowed
		}
		updated.DefaultRetrieval = false
		expiresAt := command.At.UTC()
		updated.ExpiresAt = &expiresAt

	case GovernanceDelete:
		if updated.Status != StatusRejected && updated.Status != StatusExpired {
			if err := updated.Transition(StatusExpired); err != nil {
				return GovernanceMutation{}, ErrGovernanceActionNotAllowed
			}
		}
		updated.DefaultRetrieval = false
		updated.Content = json.RawMessage(`{}`)
		updated.ContentText = ""
		updated.Applicability = Applicability{}
		updated.Provenance = Provenance{}
		updated.ParentID = ""
		deletedAt := command.At.UTC()
		updated.DeletedAt = &deletedAt
		updated.ExpiresAt = &deletedAt
		mutation.PurgeDerivedData = true
		mutation.InvalidateEmbedding = true
		changes.DerivedDataPurged = true

	case GovernanceRedact:
		if command.Redaction == nil || !json.Valid(command.Redaction.Content) || command.Redaction.Applicability.Validate() != nil {
			return GovernanceMutation{}, ErrInvalidGovernanceCommand
		}
		updated.Content = append(json.RawMessage(nil), command.Redaction.Content...)
		updated.ContentText = command.Redaction.ContentText
		updated.Applicability = command.Redaction.Applicability
		// Keep lineage but discard free-form provenance text that could repeat
		// the content being redacted.
		updated.Provenance.Reason = ""
		changes.Redacted = true
		mutation.InvalidateEmbedding = true

	case GovernanceCorrect, GovernanceSupersede:
		if command.Replacement == nil || command.RelationID == "" {
			return GovernanceMutation{}, ErrInvalidGovernanceCommand
		}
		if err := updated.Transition(StatusExpired); err != nil {
			return GovernanceMutation{}, ErrGovernanceActionNotAllowed
		}
		updated.DefaultRetrieval = false
		expiresAt := command.At.UTC()
		updated.ExpiresAt = &expiresAt

		replacement, relation, err := buildReplacement(current, *command.Replacement, command, command.Action)
		if err != nil {
			return GovernanceMutation{}, err
		}
		mutation.Replacement = &replacement
		mutation.Relation = &relation
		changes.RelatedResourceID = replacement.ID
		changes.RelatedResourceVersion = replacement.Version
	}

	mutation.Memory = updated
	changesJSON, err := json.Marshal(changes)
	if err != nil {
		return GovernanceMutation{}, ErrInvalidGovernanceCommand
	}
	mutation.Audit = GovernanceAuditEvent{
		ID: command.AuditID, ActorType: command.ActorType, ActorID: command.ActorID,
		Action: string(command.Action), ResourceType: "memory", ResourceID: current.ID,
		RequestID: command.RequestID, Changes: changesJSON, CreatedAt: command.At.UTC(),
	}
	return mutation, nil
}

func validateGovernanceCommand(current Memory, command GovernanceCommand) error {
	if current.Validate() != nil || current.Version <= 0 || current.DeletedAt != nil {
		return ErrInvalidGovernanceCommand
	}
	if command.ExpectedVersion <= 0 {
		return ErrInvalidGovernanceCommand
	}
	if command.ExpectedVersion != current.Version {
		return ErrGovernanceVersionConflict
	}
	if command.AuditID == "" || command.ActorID == "" || command.RequestID == "" || command.At.IsZero() {
		return ErrInvalidGovernanceCommand
	}
	switch command.ActorType {
	case "user", "agent", "system", "admin":
	default:
		return ErrInvalidGovernanceCommand
	}
	switch command.Action {
	case GovernanceReject, GovernanceCorrect, GovernanceExpire, GovernanceDelete, GovernanceRedact, GovernanceSupersede:
		return nil
	default:
		return ErrInvalidGovernanceCommand
	}
}

func buildReplacement(current, input Memory, command GovernanceCommand, action GovernanceAction) (Memory, Relation, error) {
	if input.ID == current.ID || input.UserID != current.UserID || input.ScopeType != current.ScopeType ||
		input.ScopeID != current.ScopeID || input.SessionID != current.SessionID {
		return Memory{}, Relation{}, ErrInvalidGovernanceCommand
	}
	if input.Validate() != nil || input.DeletedAt != nil {
		return Memory{}, Relation{}, ErrInvalidGovernanceCommand
	}
	replacement := input
	replacement.Version = 1
	replacement.CreatedAt = command.At.UTC()
	replacement.UpdatedAt = command.At.UTC()
	replacement.ExpiresAt = nil
	replacement.DeletedAt = nil
	replacement.Provenance.SourceEventIDs = mergeIDs(current.Provenance.SourceEventIDs, input.Provenance.SourceEventIDs)
	if replacement.Provenance.RunID == "" {
		replacement.Provenance.RunID = current.Provenance.RunID
	}
	if action == GovernanceCorrect {
		replacement.Status = StatusStable
		replacement.Confidence = 1
		replacement.DefaultRetrieval = true
	}
	if replacement.Status != StatusCandidate && replacement.Status != StatusActive && replacement.Status != StatusStable {
		return Memory{}, Relation{}, ErrInvalidGovernanceCommand
	}
	if replacement.Status == StatusCandidate {
		replacement.DefaultRetrieval = false
	}
	if replacement.Validate() != nil {
		return Memory{}, Relation{}, ErrInvalidGovernanceCommand
	}
	relation := Relation{
		ID: command.RelationID, SourceMemoryID: replacement.ID, TargetMemoryID: current.ID,
		Type: RelationSupersedes, Confidence: 1, CreatedAt: command.At.UTC(),
	}
	return replacement, relation, nil
}

func mergeIDs(first, second []string) []string {
	seen := make(map[string]struct{}, len(first)+len(second))
	merged := make([]string, 0, len(first)+len(second))
	for _, group := range [][]string{first, second} {
		for _, id := range group {
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
	}
	return merged
}
