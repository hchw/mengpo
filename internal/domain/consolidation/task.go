// Package consolidation defines when memory consolidation runs and how a
// MemoryTask moves through its retryable lifecycle.
package consolidation

import "errors"

var (
	ErrInvalidTrigger        = errors.New("invalid consolidation trigger")
	ErrInvalidTaskTransition = errors.New("invalid memory task transition")
)

// TriggerKind is one of the five consolidation triggers. Every trigger produces
// the same MemoryTask so the background pipeline is uniform.
type TriggerKind string

const (
	TriggerPerTurn               TriggerKind = "per_turn"
	TriggerCompression           TriggerKind = "compression"
	TriggerSessionEnd            TriggerKind = "session_end"
	TriggerSpecialEvent          TriggerKind = "special_event"
	TriggerBackgroundMaintenance TriggerKind = "background_maintenance"
)

var triggerOrder = []TriggerKind{
	TriggerPerTurn, TriggerCompression, TriggerSessionEnd, TriggerSpecialEvent, TriggerBackgroundMaintenance,
}

func ValidTrigger(trigger TriggerKind) bool {
	for _, known := range triggerOrder {
		if known == trigger {
			return true
		}
	}
	return false
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskRunning    TaskStatus = "running"
	TaskSucceeded  TaskStatus = "succeeded"
	TaskFailed     TaskStatus = "failed"
	TaskDeadLetter TaskStatus = "dead_letter"
)

// MemoryTask tracks one consolidation run. Failed tasks retry until the attempt
// budget is exhausted, then move to dead_letter for operator review.
type MemoryTask struct {
	ID          string
	TenantID    string
	Trigger     TriggerKind
	Status      TaskStatus
	Attempts    int
	MaxAttempts int
}

const DefaultMaxAttempts = 5

func NewTask(id, tenantID string, trigger TriggerKind) (MemoryTask, error) {
	if id == "" || tenantID == "" || !ValidTrigger(trigger) {
		return MemoryTask{}, ErrInvalidTrigger
	}
	return MemoryTask{ID: id, TenantID: tenantID, Trigger: trigger, Status: TaskPending, MaxAttempts: DefaultMaxAttempts}, nil
}

func (t *MemoryTask) Start() error {
	if t.Status != TaskPending {
		return ErrInvalidTaskTransition
	}
	t.Status = TaskRunning
	return nil
}

func (t *MemoryTask) Succeed() error {
	if t.Status != TaskRunning {
		return ErrInvalidTaskTransition
	}
	t.Status = TaskSucceeded
	return nil
}

// Fail records one failed attempt. It retries while attempts remain and only
// dead-letters once the budget is exhausted, so a transient provider outage does
// not permanently drop a consolidation run.
func (t *MemoryTask) Fail() error {
	if t.Status != TaskRunning && t.Status != TaskFailed {
		return ErrInvalidTaskTransition
	}
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = DefaultMaxAttempts
	}
	t.Attempts++
	if t.Attempts >= t.MaxAttempts {
		t.Status = TaskDeadLetter
		return nil
	}
	t.Status = TaskFailed
	return nil
}

// ReplayDeadLetter is the only way out of dead_letter, and it restarts the
// attempt budget.
func (t *MemoryTask) ReplayDeadLetter() error {
	if t.Status != TaskDeadLetter {
		return ErrInvalidTaskTransition
	}
	t.Status = TaskPending
	t.Attempts = 0
	return nil
}
