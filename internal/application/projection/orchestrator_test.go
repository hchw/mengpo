package projection

import (
	"reflect"
	"testing"
)

func basePolicy() Policy {
	return Policy{AllowDivergenceHint: true, AllowWeakCandidateRecall: true, MaxCandidateBudget: 90, MaxRankingBudget: 60, MaxInjectionTokenBudget: 4096, MaxRelationDepth: 5}
}

func TestChooseFocusForClearTaskAndAppliesSafeFocusHint(t *testing.T) {
	decision, err := NewOrchestrator(basePolicy()).Choose(Signals{Clarity: ClarityClear, ProgressPercent: 70, Hint: "focus", HintTopics: []string{"tenant routing"}, HintMemoryIDs: []string{"m1"}, CandidateBudget: 30, RankingBudget: 20, InjectionTokenBudget: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeFocus || !decision.HintApplied || decision.HintRejected != "" {
		t.Fatalf("decision=%#v", decision)
	}
	if decision.Scope.FocusPreference != 1 || !reflect.DeepEqual(decision.Scope.FocusHintTopics, []string{"tenant routing"}) || !reflect.DeepEqual(decision.Scope.FocusHintMemoryIDs, []string{"m1"}) {
		t.Fatalf("focus hint not preserved: %#v", decision.Scope)
	}
	if decision.Scope.CandidateLimit != 30 || decision.Scope.RankingLimit != 20 || decision.Scope.InjectionTokenBudget != 1024 {
		t.Fatalf("budgets=%#v", decision.Scope)
	}
}

func TestSafetyTriggersForceDivergenceButRetainFocusPreference(t *testing.T) {
	orchestrator := NewOrchestrator(basePolicy())
	cases := []struct {
		name    string
		signals Signals
		trigger string
	}{
		{"repeated failures", Signals{Clarity: ClarityClear, ProgressPercent: 65, RepeatedFailures: 2, Hint: "focus", HintTopics: []string{"current task"}}, "repeated-failures"},
		{"conflict", Signals{Clarity: ClarityClear, ProgressPercent: 65, ConflictCount: 1, Hint: "focus"}, "conflicting-evidence"},
		{"evidence gap", Signals{Clarity: ClarityClear, ProgressPercent: 65, EvidenceGapCount: 1, Hint: "focus"}, "evidence-gap"},
		{"unclear low progress", Signals{Clarity: ClarityUnclear, ProgressPercent: 10, Hint: "focus"}, "unclear-task-low-progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := orchestrator.Choose(tc.signals)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Mode != ModeDivergence {
				t.Fatalf("mode=%s want diverge", decision.Mode)
			}
			if !contains(decision.Triggers, tc.trigger) {
				t.Fatalf("triggers=%v missing %s", decision.Triggers, tc.trigger)
			}
			if tc.signals.Hint == "focus" && (!decision.HintApplied || decision.Scope.FocusPreference != .75) {
				t.Fatalf("safe focus preference lost: %#v", decision)
			}
			if tc.signals.Hint == "focus" && !contains(decision.Scope.FocusHintTopics, "current task") && tc.name == "repeated failures" {
				t.Fatalf("hint topics lost: %#v", decision.Scope)
			}
			if decision.Scope.RelationDepth != 5 {
				t.Fatalf("divergence relation depth=%d", decision.Scope.RelationDepth)
			}
		})
	}
}

func TestCandidateRecallOnlyEnabledForDivergenceByPolicy(t *testing.T) {
	policy := basePolicy()
	policy.AllowWeakCandidateRecall = false
	orchestrator := NewOrchestrator(policy)
	focus, err := orchestrator.Choose(Signals{Clarity: ClarityClear, ProgressPercent: 90})
	if err != nil {
		t.Fatal(err)
	}
	diverge, err := orchestrator.Choose(Signals{Clarity: ClarityClear, ProgressPercent: 90, RepeatedFailures: 2})
	if err != nil {
		t.Fatal(err)
	}
	for name, decision := range map[string]Decision{"focus": focus, "diverge": diverge} {
		if decision.Scope.IncludeCandidates || contains(decision.Scope.Statuses, "candidate") {
			t.Fatalf("%s unexpectedly includes candidate memories: %#v", name, decision.Scope)
		}
	}
}

func TestDivergenceHintRequiresPolicyButCannotOverrideSafety(t *testing.T) {
	policy := basePolicy()
	policy.AllowDivergenceHint = false
	orchestrator := NewOrchestrator(policy)
	decision, err := orchestrator.Choose(Signals{Clarity: ClarityClear, ProgressPercent: 80, Hint: "diverge"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeFocus || decision.HintApplied || decision.HintRejected == "" {
		t.Fatalf("disallowed hint decision=%#v", decision)
	}
	decision, err = orchestrator.Choose(Signals{Clarity: ClarityClear, ProgressPercent: 80, RepeatedFailures: 2, Hint: "diverge"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeDivergence || !decision.HintApplied {
		t.Fatalf("safety decision=%#v", decision)
	}
}

func TestChooseRejectsInvalidSignalsHintsAndBudgets(t *testing.T) {
	orchestrator := NewOrchestrator(basePolicy())
	cases := []Signals{
		{Clarity: "unknown"},
		{Clarity: ClarityClear, ProgressPercent: 101},
		{Clarity: ClarityClear, RepeatedFailures: -1},
		{Clarity: ClarityClear, Hint: "force"},
		{Clarity: ClarityClear, CandidateBudget: -1},
		{Clarity: ClarityClear, RankingBudget: -1},
		{Clarity: ClarityClear, InjectionTokenBudget: -1},
	}
	for _, signals := range cases {
		if _, err := orchestrator.Choose(signals); err == nil {
			t.Errorf("Choose(%#v) accepted invalid signals", signals)
		}
	}
}

func TestBudgetsAreCappedAndSplitBetweenTaskAndUserBehavior(t *testing.T) {
	decision, err := NewOrchestrator(basePolicy()).Choose(Signals{Clarity: ClarityClear, ProgressPercent: 80, CandidateBudget: 1000, RankingBudget: 1000, InjectionTokenBudget: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Scope.CandidateLimit != 90 || decision.Scope.RankingLimit != 60 || decision.Scope.InjectionTokenBudget != 4096 {
		t.Fatalf("caps not applied: %#v", decision.Scope)
	}
	if decision.Scope.TaskChannelBudget+decision.Scope.UserBehaviorBudget > decision.Scope.CandidateLimit {
		t.Fatalf("channel budgets exceed candidate budget: %#v", decision.Scope)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
