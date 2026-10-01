package projection

import (
	"encoding/json"
	"testing"

	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

func budgetCandidate(id, text string, score float64) recall.RankedCandidate {
	return recall.RankedCandidate{RecallCandidate: ports.RecallCandidate{Node: ports.MemoryNodeRecord{ID: id, IdempotencyKey: "key-" + id, ContentText: text}, Channels: []string{ports.RecallChannelFullText}, Score: score}, FinalScore: score, Included: true}
}

func TestBudgetManagerAppliesCandidateRankingAndInjectionBudgets(t *testing.T) {
	manager := NewBudgetManager()
	candidates := []recall.RankedCandidate{budgetCandidate("a", "alpha", .9), budgetCandidate("b", "bravo", .8), budgetCandidate("c", "charlie", .7)}
	result, err := manager.Select(candidates, Budget{CandidateLimit: 3, RankingLimit: 2, InjectionTokenLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.CandidatesSeen != 3 || result.Usage.CandidatesRanked != 2 || result.Usage.CandidatesInjected != 2 {
		t.Fatalf("usage=%#v", result.Usage)
	}
	if len(result.Selected) != 2 || len(result.Excluded) != 1 || result.Excluded[0].ExcludedReason != "ranking-budget" {
		t.Fatalf("selected/excluded=%#v %#v", result.Selected, result.Excluded)
	}
}

func TestBudgetManagerDeduplicatesAndPreservesPriorExclusionReasons(t *testing.T) {
	manager := NewBudgetManager()
	first := budgetCandidate("a", "alpha", .4)
	second := budgetCandidate("a", "alpha", .9)
	second.Channels = append(second.Channels, ports.RecallChannelVector)
	blocked := budgetCandidate("blocked", "secret", 1)
	blocked.Included = false
	blocked.ExcludedReason = "cross-session"
	result, err := manager.Select([]recall.RankedCandidate{first, second, blocked}, Budget{CandidateLimit: 5, RankingLimit: 5, InjectionTokenLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.CandidatesSeen != 2 || len(result.Selected) != 1 || result.Selected[0].Candidate.FinalScore != .9 {
		t.Fatalf("dedupe result=%#v", result)
	}
	if len(result.Excluded) != 1 || result.Excluded[0].ExcludedReason != "cross-session" {
		t.Fatalf("excluded=%#v", result.Excluded)
	}
}

func TestBudgetManagerUsesSummaryThenTruncatesWithoutExceedingTokens(t *testing.T) {
	manager := NewBudgetManager()
	withSummary := budgetCandidate("summary", "这是一个很长的记忆正文", .9)
	withSummary.Node.Content = json.RawMessage(`{"summary":"摘要可注入"}`)
	long := budgetCandidate("long", "0123456789", .8)
	result, err := manager.Select([]recall.RankedCandidate{withSummary, long}, Budget{CandidateLimit: 5, RankingLimit: 5, InjectionTokenLimit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 2 {
		t.Fatalf("selected=%d: %#v", len(result.Selected), result)
	}
	if !result.Selected[0].SummaryReplaced || result.Selected[0].Text != "摘要可注入" {
		t.Fatalf("summary not selected: %#v", result.Selected[0])
	}
	if !result.Selected[1].Truncated {
		t.Fatalf("long text not truncated: %#v", result.Selected[1])
	}
	if result.Usage.TokensInjected > 8 {
		t.Fatalf("token budget exceeded: %#v", result.Usage)
	}
	if estimateTokens(result.Selected[1].Text) > result.Selected[1].TokenCost {
		t.Fatal("token cost underestimates emitted text")
	}
}

func TestBudgetManagerExplainsCandidateAndInjectionExclusions(t *testing.T) {
	manager := NewBudgetManager()
	result, err := manager.Select([]recall.RankedCandidate{budgetCandidate("a", "one", .9), budgetCandidate("b", "two", .8), budgetCandidate("c", "three", .7)}, Budget{CandidateLimit: 1, RankingLimit: 3, InjectionTokenLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 1 || result.Usage.TokensInjected > 3 {
		t.Fatalf("result=%#v", result)
	}
	if len(result.Excluded) != 2 {
		t.Fatalf("excluded=%#v", result.Excluded)
	}
	if result.Excluded[0].ExcludedReason != "candidate-budget" {
		t.Fatalf("first exclusion=%q", result.Excluded[0].ExcludedReason)
	}
	if result.Excluded[1].ExcludedReason != "candidate-budget" {
		t.Fatalf("second exclusion=%q", result.Excluded[1].ExcludedReason)
	}
}

func TestBudgetManagerRejectsInvalidBudgetsAndHandlesMixedText(t *testing.T) {
	manager := NewBudgetManager()
	for _, budget := range []Budget{{}, {CandidateLimit: 1, RankingLimit: 1, InjectionTokenLimit: 0}, {CandidateLimit: -1, RankingLimit: 1, InjectionTokenLimit: 2}} {
		if _, err := manager.Select(nil, budget); err == nil {
			t.Errorf("Select(%#v) accepted", budget)
		}
	}
	text := "中文 Go 代码"
	if got := estimateTokens(text); got != 6 {
		t.Fatalf("estimateTokens(%q)=%d want 6", text, got)
	}
	candidate := budgetCandidate("c", text, .5)
	result, err := manager.Select([]recall.RankedCandidate{candidate}, Budget{CandidateLimit: 1, RankingLimit: 1, InjectionTokenLimit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 1 || estimateTokens(result.Selected[0].Text) > 4 {
		t.Fatalf("mixed text exceeded budget: %#v", result)
	}
}
