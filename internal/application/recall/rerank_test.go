package recall

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func TestRerankerOverridesRuleOrderWithoutPromotingExcludedCandidates(t *testing.T) {
	ruleTop := rankedNode("rule-top", nil)
	ruleTop.Node.ContentText = "vector-nearest candidate"
	ruleTop.Score = .95
	ruleSecond := rankedNode("cross-encoder-top", nil)
	ruleSecond.Node.ContentText = "query answer"
	ruleSecond.Score = .4
	excludedNode := rankedNode("excluded", nil)
	excluded := RankedCandidate{RecallCandidate: excludedNode, Included: false, ExcludedReason: "conflict"}
	base := []RankedCandidate{
		{RecallCandidate: ruleTop, FinalScore: .9, Included: true, Reason: RankReason{Relevance: .95}},
		{RecallCandidate: ruleSecond, FinalScore: .6, Included: true, Reason: RankReason{Relevance: .4}},
		excluded,
	}
	reranker := &fakeReranker{verdicts: map[string]float64{"rule-top": .1, "cross-encoder-top": .95}}
	ranked, err := RerankCandidates(context.Background(), reranker, "query", base)
	if err != nil {
		t.Fatalf("RerankCandidates(): %v", err)
	}
	if ranked[0].Node.ID != "cross-encoder-top" {
		t.Fatalf("reranker top=%s, want cross-encoder-top", ranked[0].Node.ID)
	}
	if ranked[0].RerankerScore == nil || *ranked[0].RerankerScore != .95 || ranked[0].Reason.RerankerScore == nil {
		t.Fatalf("reranker score not explained: %#v", ranked[0])
	}
	if ranked[2].Included || ranked[2].ExcludedReason != "conflict" {
		t.Fatalf("excluded candidate changed: %#v", ranked[2])
	}
}

type fixedReranker struct {
	results []ports.RerankResult
	err     error
}

func (f fixedReranker) Metadata() ports.RerankerMetadata {
	return ports.RerankerMetadata{Model: "test"}
}
func (f fixedReranker) Rank(context.Context, string, []ports.RerankCandidate) ([]ports.RerankResult, error) {
	return f.results, f.err
}

func TestRerankCandidatesReportsProviderAndContractFailures(t *testing.T) {
	candidate := rankedNode("m1", nil)
	candidate.Node.ContentText = "memory text"
	base := []RankedCandidate{{RecallCandidate: candidate, Included: true, FinalScore: .2}}
	if _, err := RerankCandidates(context.Background(), nil, "q", base); !errors.Is(err, ErrNoReranker) {
		t.Fatalf("nil reranker error=%v", err)
	}
	if _, err := RerankCandidates(context.Background(), &fakeReranker{}, "", base); !errors.Is(err, ports.ErrInvalidRerankRequest) {
		t.Fatalf("empty query error=%v", err)
	}
	if _, err := RerankCandidates(context.Background(), &fakeReranker{err: errors.New("down")}, "q", base); err == nil {
		t.Fatal("provider failure hidden")
	}
	if _, err := RerankCandidates(context.Background(), fixedReranker{}, "q", base); !errors.Is(err, ErrInvalidRerankerResult) {
		t.Fatalf("missing result error=%v", err)
	}
	bad := fixedReranker{results: []ports.RerankResult{{ID: "unknown", Relevance: .9}}}
	if _, err := RerankCandidates(context.Background(), bad, "q", base); !errors.Is(err, ErrInvalidRerankerResult) {
		t.Fatalf("unknown result id error=%v", err)
	}
}

func TestRerankerRerankingStableForEqualScores(t *testing.T) {
	first := rankedNode("first", nil)
	first.Node.ContentText = "first"
	second := rankedNode("second", nil)
	second.Node.ContentText = "second"
	first.Node.UpdatedAt = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	second.Node.UpdatedAt = time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	base := []RankedCandidate{
		{RecallCandidate: first, Included: true, FinalScore: .5},
		{RecallCandidate: second, Included: true, FinalScore: .5},
	}
	reranker := &fakeReranker{verdicts: map[string]float64{"first": .8, "second": .8}}
	ranked, err := RerankCandidates(context.Background(), reranker, "q", base)
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].Node.ID != "second" {
		t.Fatalf("tie should use recency; top=%s", ranked[0].Node.ID)
	}
}
