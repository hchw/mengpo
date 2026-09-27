package memory

import "testing"

func TestMemoryStatusTransitions(t *testing.T) {
	valid := []struct {
		from MemoryStatus
		to   MemoryStatus
	}{
		{StatusCandidate, StatusActive},
		{StatusCandidate, StatusConflicted},
		{StatusCandidate, StatusRejected},
		{StatusCandidate, StatusExpired},
		{StatusActive, StatusStable},
		{StatusActive, StatusConflicted},
		{StatusActive, StatusExpired},
		{StatusActive, StatusRejected},
		{StatusStable, StatusConflicted},
		{StatusStable, StatusExpired},
		{StatusStable, StatusRejected},
		{StatusConflicted, StatusExpired},
		{StatusConflicted, StatusRejected},
	}
	for _, transition := range valid {
		t.Run(string(transition.from)+"_to_"+string(transition.to), func(t *testing.T) {
			memory := Memory{Status: transition.from}
			if err := memory.Transition(transition.to); err != nil {
				t.Fatalf("Transition(%q) error = %v", transition.to, err)
			}
			if memory.Status != transition.to {
				t.Fatalf("status = %q, want %q", memory.Status, transition.to)
			}
		})
	}
}

func TestMemoryStatusRejectsIllegalAndTerminalTransitions(t *testing.T) {
	invalid := []struct {
		from MemoryStatus
		to   MemoryStatus
	}{
		{StatusCandidate, StatusStable},
		{StatusActive, StatusCandidate},
		{StatusStable, StatusActive},
		{StatusConflicted, StatusActive},
		{StatusRejected, StatusActive},
		{StatusRejected, StatusExpired},
		{StatusExpired, StatusActive},
		{StatusExpired, StatusRejected},
		{MemoryStatus("unknown"), StatusActive},
	}
	for _, transition := range invalid {
		t.Run(string(transition.from)+"_to_"+string(transition.to), func(t *testing.T) {
			memory := Memory{Status: transition.from}
			if err := memory.Transition(transition.to); err != ErrInvalidStatusTransition {
				t.Fatalf("Transition(%q) error = %v, want ErrInvalidStatusTransition", transition.to, err)
			}
			if memory.Status != transition.from {
				t.Fatalf("invalid transition mutated status to %q", memory.Status)
			}
		})
	}
}

func TestTransitionRejectsNilMemory(t *testing.T) {
	var memory *Memory
	if err := memory.Transition(StatusActive); err != ErrInvalidStatusTransition {
		t.Fatalf("nil memory transition error = %v, want ErrInvalidStatusTransition", err)
	}
}
