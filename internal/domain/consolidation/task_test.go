package consolidation

import (
	"errors"
	"testing"
)

func TestAllFiveTriggersProduceUniformTasks(t *testing.T) {
	for _, trigger := range triggerOrder {
		task, err := NewTask("task-1", "tenant-a", trigger)
		if err != nil {
			t.Fatalf("trigger %s: %v", trigger, err)
		}
		if task.Status != TaskPending || task.Trigger != trigger {
			t.Fatalf("task=%#v", task)
		}
		if !ValidTrigger(trigger) {
			t.Fatalf("trigger %s not recognized", trigger)
		}
	}
	if _, err := NewTask("task-1", "tenant-a", "unknown"); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("unknown trigger err=%v", err)
	}
}

func TestTaskLifecycleSucceedsAndDeadLettersAfterBudget(t *testing.T) {
	task, _ := NewTask("task-1", "tenant-a", TriggerPerTurn)
	task.MaxAttempts = 3
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	if err := task.Fail(); err != nil || task.Status != TaskFailed || task.Attempts != 1 {
		t.Fatalf("first failure task=%#v err=%v", task, err)
	}
	if err := task.Fail(); err != nil || task.Status != TaskFailed || task.Attempts != 2 {
		t.Fatalf("second failure task=%#v err=%v", task, err)
	}
	if err := task.Fail(); err != nil || task.Status != TaskDeadLetter {
		t.Fatalf("exhausted task=%#v err=%v", task, err)
	}
	if err := task.Succeed(); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("dead-letter succeed err=%v", err)
	}
	if err := task.ReplayDeadLetter(); err != nil || task.Status != TaskPending || task.Attempts != 0 {
		t.Fatalf("replay task=%#v err=%v", task, err)
	}
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	if err := task.Succeed(); err != nil || task.Status != TaskSucceeded {
		t.Fatalf("succeed task=%#v err=%v", task, err)
	}
}

func TestTaskRejectsOutOfOrderTransitions(t *testing.T) {
	task, _ := NewTask("task-1", "tenant-a", TriggerCompression)
	if err := task.Succeed(); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("succeed before start err=%v", err)
	}
	if err := task.Fail(); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("fail before start err=%v", err)
	}
	if err := task.ReplayDeadLetter(); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("replay before dead-letter err=%v", err)
	}
}
