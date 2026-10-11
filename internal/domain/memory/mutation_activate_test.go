package memory

import (
	"errors"
	"testing"
	"time"
)

func TestGovernanceActivatePromotesCandidate(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	current := mutationMemory(StatusCandidate)
	command := baseMutationCommand(GovernanceActivate, now)
	command.ActorType = "system"
	command.Promotion = &GovernancePromotion{TargetStatus: StatusActive, Confidence: 0.9, Reason: "independent_session_recurrence"}
	mutation, err := ApplyGovernanceAction(current, command)
	if err != nil {
		t.Fatalf("ApplyGovernanceAction() error = %v", err)
	}
	if mutation.Memory.Status != StatusActive || !mutation.Memory.DefaultRetrieval || mutation.Memory.Confidence != 0.9 {
		t.Fatalf("mutation = %#v", mutation.Memory)
	}
	if mutation.Memory.Version != current.Version+1 {
		t.Fatalf("version = %d, want %d", mutation.Memory.Version, current.Version+1)
	}
}

func TestGovernanceActivateRequiresPromotion(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	command := baseMutationCommand(GovernanceActivate, now)
	if _, err := ApplyGovernanceAction(mutationMemory(StatusCandidate), command); !errors.Is(err, ErrInvalidGovernanceCommand) {
		t.Fatalf("error = %v, want ErrInvalidGovernanceCommand", err)
	}
}

func TestGovernanceActivateRejectsIllegalTarget(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	command := baseMutationCommand(GovernanceActivate, now)
	command.Promotion = &GovernancePromotion{TargetStatus: StatusCandidate, Confidence: 1}
	if _, err := ApplyGovernanceAction(mutationMemory(StatusCandidate), command); !errors.Is(err, ErrInvalidGovernanceCommand) {
		t.Fatalf("error = %v, want ErrInvalidGovernanceCommand", err)
	}
}

func TestGovernanceActivateIsIdempotentForSameStatus(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	current := mutationMemory(StatusActive)
	command := baseMutationCommand(GovernanceActivate, now)
	command.Promotion = &GovernancePromotion{TargetStatus: StatusActive, Confidence: 1, Reason: "explicit_user_confirmation"}
	mutation, err := ApplyGovernanceAction(current, command)
	if err != nil {
		t.Fatalf("ApplyGovernanceAction() error = %v", err)
	}
	if mutation.Memory.Status != StatusActive || !mutation.Memory.DefaultRetrieval {
		t.Fatalf("mutation = %#v", mutation.Memory)
	}
}
