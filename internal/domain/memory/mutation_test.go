package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApplyGovernanceActionStateAndVersionChanges(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		status        MemoryStatus
		action        GovernanceAction
		wantStatus    MemoryStatus
		wantRetrieval bool
		deleted       bool
	}{
		{name: "reject candidate", status: StatusCandidate, action: GovernanceReject, wantStatus: StatusRejected},
		{name: "expire stable", status: StatusStable, action: GovernanceExpire, wantStatus: StatusExpired},
		{name: "delete active", status: StatusActive, action: GovernanceDelete, wantStatus: StatusExpired, deleted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := mutationMemory(test.status)
			mutation, err := ApplyGovernanceAction(current, baseMutationCommand(test.action, now))
			if err != nil {
				t.Fatalf("ApplyGovernanceAction() error = %v", err)
			}
			if mutation.Memory.Status != test.wantStatus || mutation.Memory.Version != current.Version+1 || mutation.Memory.DefaultRetrieval != test.wantRetrieval {
				t.Fatalf("mutation memory = %#v", mutation.Memory)
			}
			if mutation.Audit.Action != string(test.action) || mutation.Audit.ResourceID != current.ID || mutation.Audit.CreatedAt != now {
				t.Fatalf("audit event = %#v", mutation.Audit)
			}
			var changes GovernanceAuditChanges
			if err := json.Unmarshal(mutation.Audit.Changes, &changes); err != nil {
				t.Fatalf("decode audit changes: %v", err)
			}
			if changes.BeforeVersion != current.Version || changes.AfterVersion != current.Version+1 {
				t.Fatalf("audit versions = %d -> %d", changes.BeforeVersion, changes.AfterVersion)
			}
			if test.deleted {
				if mutation.Memory.DeletedAt == nil || mutation.Memory.ContentText != "" || string(mutation.Memory.Content) != `{}` || len(mutation.Memory.Provenance.SourceEventIDs) != 0 || mutation.Memory.Provenance.RunID != "" || mutation.Memory.Provenance.Analyst != "" || mutation.Memory.Provenance.ModelVersion != "" || mutation.Memory.Provenance.Reason != "" || !mutation.PurgeDerivedData || !mutation.InvalidateEmbedding || !changes.DerivedDataPurged {
					t.Fatalf("delete did not scrub content and request derived-data purge: %#v", mutation)
				}
			} else if mutation.Memory.DeletedAt != nil || mutation.PurgeDerivedData {
				t.Fatalf("non-delete action unexpectedly deleted data: %#v", mutation)
			}
		})
	}
}

func TestApplyGovernanceActionRedactsContentAndInvalidatesEmbedding(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	current := mutationMemory(StatusStable)
	current.Provenance.Reason = "private original content"
	mutation, err := ApplyGovernanceAction(current, GovernanceCommand{
		Action: GovernanceRedact, ExpectedVersion: current.Version, AuditID: "audit-1",
		ActorType: "user", ActorID: "user-1", RequestID: "request-1", At: now,
		Redaction: &MemoryRedaction{
			Content: json.RawMessage(`{"text":"[redacted]"}`), ContentText: "[redacted]",
			Applicability: Applicability{Conditions: []string{"safe-context"}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyGovernanceAction() error = %v", err)
	}
	if mutation.Memory.Status != StatusStable || mutation.Memory.DefaultRetrieval != current.DefaultRetrieval || mutation.Memory.Version != current.Version+1 {
		t.Fatalf("redaction changed governance state unexpectedly: %#v", mutation.Memory)
	}
	if mutation.Memory.ContentText != "[redacted]" || mutation.Memory.Provenance.Reason != "" || !mutation.InvalidateEmbedding || mutation.PurgeDerivedData {
		t.Fatalf("redaction result = %#v", mutation)
	}
	if strings.Contains(string(mutation.Audit.Changes), "private original content") || strings.Contains(string(mutation.Audit.Changes), "[redacted]") {
		t.Fatalf("audit changes leaked content: %s", mutation.Audit.Changes)
	}
}

func TestApplyGovernanceActionCorrectCreatesStableSupersedingMemory(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	current := mutationMemory(StatusStable)
	current.Provenance.SourceEventIDs = []string{"event-old"}
	replacement := mutationMemory(StatusCandidate)
	replacement.ID = "memory-new"
	replacement.IdempotencyKey = "idem-new"
	replacement.Content = json.RawMessage(`{"text":"corrected preference"}`)
	replacement.ContentText = "corrected preference"
	replacement.Provenance.SourceEventIDs = []string{"event-correction", "event-old"}
	command := baseMutationCommand(GovernanceCorrect, now)
	command.RelationID = "relation-1"
	command.Replacement = &replacement
	mutation, err := ApplyGovernanceAction(current, command)
	if err != nil {
		t.Fatalf("ApplyGovernanceAction() error = %v", err)
	}
	if mutation.Memory.Status != StatusExpired || mutation.Memory.DefaultRetrieval || mutation.Memory.Version != current.Version+1 {
		t.Fatalf("superseded memory = %#v", mutation.Memory)
	}
	if mutation.Replacement == nil || mutation.Replacement.Status != StatusStable || mutation.Replacement.Confidence != 1 || !mutation.Replacement.DefaultRetrieval || mutation.Replacement.Version != 1 {
		t.Fatalf("corrected replacement = %#v", mutation.Replacement)
	}
	if got := mutation.Replacement.Provenance.SourceEventIDs; len(got) != 2 || got[0] != "event-old" || got[1] != "event-correction" {
		t.Fatalf("replacement provenance = %#v", got)
	}
	if mutation.Relation == nil || mutation.Relation.Type != RelationSupersedes || mutation.Relation.SourceMemoryID != replacement.ID || mutation.Relation.TargetMemoryID != current.ID {
		t.Fatalf("supersedes relation = %#v", mutation.Relation)
	}
	var changes GovernanceAuditChanges
	if err := json.Unmarshal(mutation.Audit.Changes, &changes); err != nil {
		t.Fatalf("decode audit changes: %v", err)
	}
	if changes.RelatedResourceID != replacement.ID || changes.RelatedResourceVersion != 1 {
		t.Fatalf("audit does not identify replacement version: %#v", changes)
	}
}

func TestApplyGovernanceActionSupersedeKeepsReplacementAsCandidate(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	current := mutationMemory(StatusActive)
	replacement := mutationMemory(StatusCandidate)
	replacement.ID = "memory-next"
	replacement.IdempotencyKey = "idem-next"
	command := baseMutationCommand(GovernanceSupersede, now)
	command.RelationID = "relation-next"
	command.Replacement = &replacement
	mutation, err := ApplyGovernanceAction(current, command)
	if err != nil {
		t.Fatalf("ApplyGovernanceAction() error = %v", err)
	}
	if mutation.Replacement.Status != StatusCandidate || mutation.Replacement.DefaultRetrieval || mutation.Memory.Status != StatusExpired {
		t.Fatalf("supersede mutation = %#v", mutation)
	}
}

func TestApplyGovernanceActionRejectsStaleAndUnsafeCommands(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	current := mutationMemory(StatusStable)
	stale := baseMutationCommand(GovernanceReject, now)
	stale.ExpectedVersion--
	if _, err := ApplyGovernanceAction(current, stale); !errors.Is(err, ErrGovernanceVersionConflict) {
		t.Fatalf("stale version error = %v, want ErrGovernanceVersionConflict", err)
	}
	if _, err := ApplyGovernanceAction(mutationMemory(StatusRejected), baseMutationCommand(GovernanceExpire, now)); !errors.Is(err, ErrGovernanceActionNotAllowed) {
		t.Fatalf("terminal-state error = %v, want ErrGovernanceActionNotAllowed", err)
	}
	badRedaction := baseMutationCommand(GovernanceRedact, now)
	badRedaction.Redaction = &MemoryRedaction{Content: json.RawMessage(`not-json`)}
	if _, err := ApplyGovernanceAction(current, badRedaction); !errors.Is(err, ErrInvalidGovernanceCommand) {
		t.Fatalf("invalid redaction error = %v, want ErrInvalidGovernanceCommand", err)
	}
	badCorrection := baseMutationCommand(GovernanceCorrect, now)
	badCorrection.RelationID = "relation-bad"
	wrongScope := mutationMemory(StatusCandidate)
	wrongScope.ID = "memory-wrong-scope"
	wrongScope.IdempotencyKey = "idem-wrong-scope"
	wrongScope.UserID = "other-user"
	wrongScope.ScopeID = "other-user"
	badCorrection.Replacement = &wrongScope
	if _, err := ApplyGovernanceAction(current, badCorrection); !errors.Is(err, ErrInvalidGovernanceCommand) {
		t.Fatalf("cross-scope correction error = %v, want ErrInvalidGovernanceCommand", err)
	}
}

func mutationMemory(status MemoryStatus) Memory {
	return Memory{
		ID: "memory-1", IdempotencyKey: "idem-1", UserID: "user-1", ScopeType: ScopeUserGlobal,
		ScopeID: "user-1", Type: "preference", Status: status, Visibility: VisibilityPrivate,
		Confidence: 0.8, Content: json.RawMessage(`{"text":"private preference"}`),
		ContentText: "private preference", DefaultRetrieval: status == StatusActive || status == StatusStable,
		Version: 7, Provenance: Provenance{SourceEventIDs: []string{"event-1"}, Reason: "source note"},
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
}

func baseMutationCommand(action GovernanceAction, at time.Time) GovernanceCommand {
	return GovernanceCommand{
		Action: action, ExpectedVersion: 7, AuditID: "audit-1", ActorType: "user",
		ActorID: "user-1", RequestID: "request-1", At: at,
	}
}
