package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hchw/mengpo/internal/application/recall"
)

var ErrInvalidBudget = errors.New("invalid projection budget")

type Budget struct {
	CandidateLimit      int
	RankingLimit        int
	InjectionTokenLimit int
}

type Item struct {
	Candidate       recall.RankedCandidate
	Text            string
	TokenCost       int
	SummaryReplaced bool
	Truncated       bool
	ExcludedReason  string
}

type BudgetUsage struct {
	CandidatesSeen     int
	CandidatesRanked   int
	CandidatesInjected int
	TokensInjected     int
	SummariesReplaced  int
	Truncated          int
}

type BudgetResult struct {
	Selected []Item
	Excluded []Item
	Usage    BudgetUsage
}

// BudgetManager applies candidate, ranking and final injection budgets in
// order. Token estimation intentionally counts every non-space Unicode rune
// as one token, a conservative local estimate for mixed Chinese/code text.
type BudgetManager struct{}

func NewBudgetManager() *BudgetManager { return &BudgetManager{} }

func (m *BudgetManager) Select(ranked []recall.RankedCandidate, budget Budget) (BudgetResult, error) {
	if budget.CandidateLimit < 1 || budget.RankingLimit < 1 || budget.InjectionTokenLimit < 1 {
		return BudgetResult{}, ErrInvalidBudget
	}
	result := BudgetResult{Selected: []Item{}, Excluded: []Item{}}
	unique := dedupeRanked(ranked)
	result.Usage.CandidatesSeen = len(unique)
	eligible := make([]recall.RankedCandidate, 0, len(unique))
	for _, candidate := range unique {
		if !candidate.Included {
			result.Excluded = append(result.Excluded, Item{Candidate: candidate, ExcludedReason: candidate.ExcludedReason})
			continue
		}
		eligible = append(eligible, candidate)
	}
	for index, candidate := range eligible {
		if index >= budget.CandidateLimit {
			result.Excluded = append(result.Excluded, Item{Candidate: candidate, ExcludedReason: "candidate-budget"})
			continue
		}
		if index >= budget.RankingLimit {
			result.Excluded = append(result.Excluded, Item{Candidate: candidate, ExcludedReason: "ranking-budget"})
			continue
		}
		result.Usage.CandidatesRanked++
		text := candidate.Node.ContentText
		if strings.TrimSpace(text) == "" {
			result.Excluded = append(result.Excluded, Item{Candidate: candidate, ExcludedReason: "empty-content"})
			continue
		}
		remaining := budget.InjectionTokenLimit - result.Usage.TokensInjected
		if remaining <= 0 {
			result.Excluded = append(result.Excluded, Item{Candidate: candidate, ExcludedReason: "injection-budget"})
			continue
		}
		item := Item{Candidate: candidate, Text: text}
		if estimateTokens(text) > remaining {
			summary := extractSummary(candidate.Node.Content)
			if summary != "" && estimateTokens(summary) <= remaining {
				item.Text = summary
				item.SummaryReplaced = true
				result.Usage.SummariesReplaced++
			} else {
				item.Text = truncateToTokens(text, remaining)
				item.Truncated = true
				result.Usage.Truncated++
			}
		}
		item.TokenCost = estimateTokens(item.Text)
		if item.TokenCost == 0 {
			result.Excluded = append(result.Excluded, Item{Candidate: candidate, ExcludedReason: "empty-content"})
			continue
		}
		if item.TokenCost > remaining {
			// Defensive invariant: estimator/truncator mismatch must not leak
			// beyond the caller's injection budget.
			item.Text = truncateToTokens(item.Text, remaining)
			item.TokenCost = estimateTokens(item.Text)
			item.Truncated = true
			result.Usage.Truncated++
		}
		result.Selected = append(result.Selected, item)
		result.Usage.CandidatesInjected++
		result.Usage.TokensInjected += item.TokenCost
	}
	if result.Usage.TokensInjected > budget.InjectionTokenLimit {
		return BudgetResult{}, fmt.Errorf("%w: injection token invariant exceeded", ErrInvalidBudget)
	}
	return result, nil
}

func estimateTokens(text string) int {
	count := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			count++
		}
	}
	return count
}

func extractSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(raw, &content) != nil {
		return ""
	}
	for _, key := range []string{"summary", "abstract"} {
		var summary string
		if json.Unmarshal(content[key], &summary) == nil && strings.TrimSpace(summary) != "" {
			return strings.TrimSpace(summary)
		}
	}
	return ""
}

func truncateToTokens(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if estimateTokens(text) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	target := limit - 1 // reserve one conservative token for ellipsis
	var builder strings.Builder
	used := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			builder.WriteRune(r)
			continue
		}
		if used >= target {
			break
		}
		builder.WriteRune(r)
		used++
	}
	truncated := strings.TrimRightFunc(builder.String(), unicode.IsSpace) + "…"
	for estimateTokens(truncated) > limit && utf8.RuneCountInString(truncated) > 1 {
		runes := []rune(truncated)
		truncated = string(runes[:len(runes)-2]) + "…"
	}
	return truncated
}

func dedupeRanked(candidates []recall.RankedCandidate) []recall.RankedCandidate {
	byKey := make(map[string]recall.RankedCandidate, len(candidates))
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.Node.ID
		if candidate.Node.IdempotencyKey != "" {
			key = candidate.Node.IdempotencyKey
		}
		if key == "" {
			continue
		}
		if existing, ok := byKey[key]; ok {
			if candidate.FinalScore > existing.FinalScore {
				existing.FinalScore = candidate.FinalScore
			}
			for _, channel := range candidate.Channels {
				found := false
				for _, old := range existing.Channels {
					if old == channel {
						found = true
						break
					}
				}
				if !found {
					existing.Channels = append(existing.Channels, channel)
				}
			}
			byKey[key] = existing
			continue
		}
		byKey[key] = candidate
		keys = append(keys, key)
	}
	unique := make([]recall.RankedCandidate, 0, len(keys))
	for _, key := range keys {
		unique = append(unique, byKey[key])
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i].Included != unique[j].Included {
			return unique[i].Included
		}
		return unique[i].FinalScore > unique[j].FinalScore
	})
	return unique
}
