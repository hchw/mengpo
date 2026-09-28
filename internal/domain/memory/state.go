package memory

import "errors"

var ErrInvalidStatusTransition = errors.New("invalid memory status transition")

// CanTransition reports whether a governance action may move a memory between
// lifecycle states. Candidate promotion is intentionally two-step: active then
// stable, so unverified candidates cannot skip the review state.
func CanTransition(from, to MemoryStatus) bool {
	switch from {
	case StatusCandidate:
		return to == StatusActive || to == StatusConflicted || to == StatusRejected || to == StatusExpired
	case StatusActive:
		return to == StatusStable || to == StatusConflicted || to == StatusExpired || to == StatusRejected
	case StatusStable:
		return to == StatusConflicted || to == StatusExpired || to == StatusRejected
	case StatusConflicted:
		return to == StatusExpired || to == StatusRejected
	default:
		// Rejected and expired are terminal; unknown states are never accepted.
		return false
	}
}

func (m *Memory) Transition(to MemoryStatus) error {
	if m == nil || !CanTransition(m.Status, to) {
		return ErrInvalidStatusTransition
	}
	m.Status = to
	return nil
}
