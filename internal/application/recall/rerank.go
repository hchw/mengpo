package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidRerankerResult = errors.New("reranker result does not match the candidate set")

type RerankMeta struct {
	Mode           string
	DegradedReason string
}

// RankWithFallback runs the independent reranker after the rule-based safety
// filters. Provider errors deliberately fall back to the original deterministic
// ranking and are returned as degradation metadata rather than failing recall.
func RankWithFallback(ctx context.Context, reranker ports.Reranker, query string, candidates []RankedCandidate) ([]RankedCandidate, RerankMeta) {
	if reranker == nil {
		return candidates, RerankMeta{Mode: "explainable-rules", DegradedReason: "reranker-unavailable"}
	}
	ranked, err := RerankCandidates(ctx, reranker, query, candidates)
	if err != nil {
		return candidates, RerankMeta{Mode: "explainable-rules", DegradedReason: "reranker-failed: " + err.Error()}
	}
	return ranked, RerankMeta{Mode: "cross-encoder"}
}

// RerankCandidates applies the independent cross-encoder after authorization,
// applicability and conflict filtering. The rule score remains part of the
// final score and the raw reranker score is retained for explainability.
func RerankCandidates(ctx context.Context, reranker ports.Reranker, query string, candidates []RankedCandidate) ([]RankedCandidate, error) {
	if reranker == nil {
		return nil, ErrNoReranker
	}
	if query == "" {
		return nil, ports.ErrInvalidRerankRequest
	}
	indexes := map[string]int{}
	request := make([]ports.RerankCandidate, 0, len(candidates))
	result := append([]RankedCandidate(nil), candidates...)
	for index, candidate := range candidates {
		if !candidate.Included {
			continue
		}
		if candidate.Node.ID == "" || candidate.Node.ContentText == "" {
			return nil, ErrInvalidRerankerResult
		}
		if _, exists := indexes[candidate.Node.ID]; exists {
			return nil, ErrInvalidRerankerResult
		}
		indexes[candidate.Node.ID] = index
		request = append(request, ports.RerankCandidate{ID: candidate.Node.ID, Text: candidate.Node.ContentText})
	}
	if len(request) == 0 {
		return result, nil
	}
	scores, err := reranker.Rank(ctx, query, request)
	if err != nil {
		return nil, fmt.Errorf("rerank candidates: %w", err)
	}
	if len(scores) != len(request) {
		return nil, ErrInvalidRerankerResult
	}
	seen := map[string]bool{}
	for _, score := range scores {
		index, ok := indexes[score.ID]
		if !ok || seen[score.ID] {
			return nil, ErrInvalidRerankerResult
		}
		seen[score.ID] = true
		raw := score.Relevance
		normalized := clamp01(raw)
		result[index].RerankerScore = &raw
		result[index].Reason.RerankerScore = &raw
		result[index].FinalScore = 0.7*normalized + 0.3*result[index].FinalScore
	}
	for id := range indexes {
		if !seen[id] {
			return nil, ErrInvalidRerankerResult
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Included != result[j].Included {
			return result[i].Included
		}
		if result[i].FinalScore != result[j].FinalScore {
			return result[i].FinalScore > result[j].FinalScore
		}
		return result[i].Node.UpdatedAt.After(result[j].Node.UpdatedAt)
	})
	return result, nil
}
