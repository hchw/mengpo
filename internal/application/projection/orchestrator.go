package projection

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeFocus      Mode = "focus"
	ModeDivergence Mode = "diverge"
)

type TaskClarity string

const (
	ClarityClear   TaskClarity = "clear"
	ClarityPartial TaskClarity = "partial"
	ClarityUnclear TaskClarity = "unclear"
)

type Signals struct {
	Clarity              TaskClarity
	ProgressPercent      int
	RepeatedFailures     int
	ConflictCount        int
	EvidenceGapCount     int
	Hint                 string
	HintTopics           []string
	HintMemoryIDs        []string
	CandidateBudget      int
	RankingBudget        int
	InjectionTokenBudget int
	// AllowCandidates is a caller preference for weak-candidate recall. A nil
	// value leaves the decision to policy; an explicit false denies candidates
	// even when policy would otherwise allow them.
	AllowCandidates *bool
}

type Policy struct {
	AllowDivergenceHint      bool
	AllowWeakCandidateRecall bool
	MaxCandidateBudget       int
	MaxRankingBudget         int
	MaxInjectionTokenBudget  int
	MaxRelationDepth         int
}

type CandidateScope struct {
	Statuses             []string
	IncludeUserGlobal    bool
	IncludeSession       bool
	IncludeCandidates    bool
	CandidateLimit       int
	RankingLimit         int
	RelationDepth        int
	TaskChannelBudget    int
	UserBehaviorBudget   int
	InjectionTokenBudget int
	FocusHintTopics      []string
	FocusHintMemoryIDs   []string
	// FocusPreference affects downstream ranking only; it cannot change a
	// safety-triggered Divergence decision back into Focus.
	FocusPreference float64
}

type Decision struct {
	Mode         Mode
	Reason       string
	Triggers     []string
	HintApplied  bool
	HintRejected string
	Scope        CandidateScope
}

// Orchestrator selects retrieval mode from objective signals. Safety triggers
// always win over Focus hints; a Focus hint remains an explicit bounded ranking
// preference so useful task context is not discarded during Divergence.
type Orchestrator struct{ policy Policy }

func NewOrchestrator(policy Policy) *Orchestrator {
	if policy.MaxCandidateBudget <= 0 {
		policy.MaxCandidateBudget = 100
	}
	if policy.MaxRankingBudget <= 0 {
		policy.MaxRankingBudget = 100
	}
	if policy.MaxInjectionTokenBudget <= 0 {
		policy.MaxInjectionTokenBudget = 8192
	}
	if policy.MaxRelationDepth <= 0 {
		policy.MaxRelationDepth = 4
	}
	return &Orchestrator{policy: policy}
}

func (o *Orchestrator) Choose(signals Signals) (Decision, error) {
	if signals.ProgressPercent < 0 || signals.ProgressPercent > 100 || signals.RepeatedFailures < 0 || signals.ConflictCount < 0 || signals.EvidenceGapCount < 0 {
		return Decision{}, fmt.Errorf("invalid orchestration signals")
	}
	if signals.Clarity != ClarityClear && signals.Clarity != ClarityPartial && signals.Clarity != ClarityUnclear {
		return Decision{}, fmt.Errorf("invalid task clarity %q", signals.Clarity)
	}
	candidateBudget, err := clampBudget(signals.CandidateBudget, o.policy.MaxCandidateBudget)
	if err != nil {
		return Decision{}, fmt.Errorf("candidate budget: %w", err)
	}
	rankingBudget, err := clampBudget(signals.RankingBudget, o.policy.MaxRankingBudget)
	if err != nil {
		return Decision{}, fmt.Errorf("ranking budget: %w", err)
	}
	injectionBudget, err := clampBudget(signals.InjectionTokenBudget, o.policy.MaxInjectionTokenBudget)
	if err != nil {
		return Decision{}, fmt.Errorf("injection budget: %w", err)
	}

	triggers := make([]string, 0, 5)
	if signals.RepeatedFailures >= 2 {
		triggers = append(triggers, "repeated-failures")
	}
	if signals.ConflictCount > 0 {
		triggers = append(triggers, "conflicting-evidence")
	}
	if signals.EvidenceGapCount > 0 {
		triggers = append(triggers, "evidence-gap")
	}
	if signals.Clarity == ClarityUnclear && signals.ProgressPercent < 50 {
		triggers = append(triggers, "unclear-task-low-progress")
	}
	if signals.Clarity == ClarityPartial && signals.ProgressPercent < 20 && signals.RepeatedFailures > 0 {
		triggers = append(triggers, "stalled-progress")
	}

	decision := Decision{Triggers: triggers}
	requestedHint := strings.TrimSpace(strings.ToLower(signals.Hint))
	switch requestedHint {
	case "", "auto", "focus", "diverge":
	default:
		return Decision{}, fmt.Errorf("unsupported retrieval hint %q", signals.Hint)
	}
	if len(triggers) > 0 {
		decision.Mode = ModeDivergence
		decision.Reason = "divergence triggered by " + strings.Join(triggers, ", ")
		if requestedHint == "focus" {
			decision.HintApplied = true
			decision.Reason += "; focus hint retained as bounded candidate preference"
			decision.Scope.FocusPreference = 0.75
			decision.Scope.FocusHintTopics = append([]string(nil), signals.HintTopics...)
			decision.Scope.FocusHintMemoryIDs = append([]string(nil), signals.HintMemoryIDs...)
		}
		if requestedHint == "diverge" {
			decision.HintApplied = true
		}
	} else if requestedHint == "diverge" {
		if o.policy.AllowDivergenceHint {
			decision.Mode = ModeDivergence
			decision.Reason = "divergence requested by permitted memory_hint"
			decision.HintApplied = true
		} else {
			decision.Mode = ModeFocus
			decision.Reason = "focus retained; divergence hint disallowed by policy"
			decision.HintRejected = "divergence hint disallowed by policy"
		}
	} else {
		decision.Mode = ModeFocus
		decision.Reason = "task is sufficiently clear without repeated failure, conflict, or evidence gap"
		if requestedHint == "focus" {
			decision.HintApplied = true
			decision.Scope.FocusPreference = 1
			decision.Scope.FocusHintTopics = append([]string(nil), signals.HintTopics...)
			decision.Scope.FocusHintMemoryIDs = append([]string(nil), signals.HintMemoryIDs...)
		}
	}

	includeCandidates := decision.Mode == ModeDivergence && o.policy.AllowWeakCandidateRecall
	if signals.AllowCandidates != nil && !*signals.AllowCandidates {
		includeCandidates = false
	}
	statuses := []string{"active", "stable"}
	if includeCandidates {
		statuses = append(statuses, "candidate")
	}
	relationDepth := 1
	if decision.Mode == ModeDivergence {
		relationDepth = o.policy.MaxRelationDepth
	}
	if relationDepth > o.policy.MaxRelationDepth {
		relationDepth = o.policy.MaxRelationDepth
	}
	decision.Scope.Statuses = statuses
	decision.Scope.IncludeUserGlobal = true
	decision.Scope.IncludeSession = true
	decision.Scope.IncludeCandidates = includeCandidates
	decision.Scope.CandidateLimit = candidateBudget
	decision.Scope.RankingLimit = rankingBudget
	decision.Scope.RelationDepth = relationDepth
	decision.Scope.TaskChannelBudget = candidateBudget * 2 / 3
	decision.Scope.UserBehaviorBudget = candidateBudget / 3
	decision.Scope.InjectionTokenBudget = injectionBudget
	return decision, nil
}

func clampBudget(requested, maximum int) (int, error) {
	if requested < 0 {
		return 0, fmt.Errorf("must be non-negative")
	}
	if requested == 0 || requested > maximum {
		return maximum, nil
	}
	return requested, nil
}
