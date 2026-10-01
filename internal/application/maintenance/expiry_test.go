package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeDueReader struct{ nodes []ports.MemoryNodeRecord }

func (f fakeDueReader) ListDueMemories(context.Context, string, int) ([]ports.MemoryNodeRecord, error) {
	return f.nodes, nil
}

type fakeGovernance struct {
	applied []string
	actions []memory.GovernanceAction
}

func (f *fakeGovernance) Apply(_ context.Context, _ string, memoryID string, command memory.GovernanceCommand) (memory.GovernanceMutation, error) {
	f.applied = append(f.applied, memoryID)
	f.actions = append(f.actions, command.Action)
	return memory.GovernanceMutation{}, nil
}

func TestExpiryJobExpiresThroughGovernance(t *testing.T) {
	governanceStub := &fakeGovernance{}
	job := &ExpiryJob{
		PlanName:   "expiry",
		Reader:     fakeDueReader{nodes: []ports.MemoryNodeRecord{{ID: "m1", Version: 2}, {ID: "m2", Version: 1}}},
		Governance: governanceStub,
		NewID:      func() (string, error) { return "00000000-0000-4000-8000-000000000000", nil },
		Clock:      func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(governanceStub.applied) != 2 {
		t.Fatalf("applied = %#v, want 2 expiries", governanceStub.applied)
	}
	for _, action := range governanceStub.actions {
		if action != memory.GovernanceExpire {
			t.Fatalf("expiry used action %q, want expire", action)
		}
	}
}

func TestExpiryJobSkipsEmptyTenant(t *testing.T) {
	governanceStub := &fakeGovernance{}
	job := &ExpiryJob{PlanName: "expiry", Reader: fakeDueReader{}, Governance: governanceStub}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(governanceStub.applied) != 0 {
		t.Fatalf("empty tenant expired %d memories, want 0", len(governanceStub.applied))
	}
}
