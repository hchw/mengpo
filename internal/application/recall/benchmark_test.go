package recall

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func benchCandidates(count int, channels []string) []ports.RecallCandidate {
	candidates := make([]ports.RecallCandidate, 0, count)
	for index := 0; index < count; index++ {
		candidates = append(candidates, ports.RecallCandidate{
			Node: ports.MemoryNodeRecord{
				ID:               fmt.Sprintf("memory-%06d", index),
				IdempotencyKey:   fmt.Sprintf("key-%06d", index),
				UserID:           "user-1",
				Status:           "active",
				ScopeType:        "user-global",
				ScopeID:          "user-1",
				MemoryType:       "preference",
				Confidence:       0.8,
				ContentText:      "retrieval precision memory content",
				DefaultRetrieval: true,
				UpdatedAt:        time.Now().UTC().Add(-time.Duration(index) * time.Minute),
			},
			Channels: channels,
			Score:    1 - float64(index)/float64(count+1),
		})
	}
	return candidates
}

// BenchmarkRankStructuredFullText is the degraded/no-embedding path: candidates
// come from structured and full-text channels only.
func BenchmarkRankStructuredFullText(b *testing.B) {
	candidates := benchCandidates(50, []string{ports.RecallChannelStructured, ports.RecallChannelFullText})
	context := RankingContext{SessionID: "session-1", MaxCandidates: 20}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Rank(candidates, context)
	}
}

// BenchmarkRankHybrid covers vector + relation + parent channels at a larger
// candidate count.
func BenchmarkRankHybrid(b *testing.B) {
	candidates := benchCandidates(200, []string{ports.RecallChannelFullText, ports.RecallChannelVector, ports.RecallChannelRelation, ports.RecallChannelParent})
	context := RankingContext{SessionID: "session-1", MaxCandidates: 20}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Rank(candidates, context)
	}
}

// BenchmarkRankDegraded measures the reranker-fallback path, where ranking and
// explainable rules still decide the order.
func BenchmarkRankDegraded(b *testing.B) {
	candidates := Rank(benchCandidates(100, []string{ports.RecallChannelFullText}), RankingContext{SessionID: "session-1", MaxCandidates: 20})
	reranker := &failingBenchReranker{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, meta := RankWithFallback(context.Background(), reranker, "query", candidates)
		if meta.Mode != "explainable-rules" {
			b.Fatalf("expected fallback mode, got %q", meta.Mode)
		}
	}
}

type failingBenchReranker struct{}

func (f *failingBenchReranker) Rank(context.Context, string, []ports.RerankCandidate) ([]ports.RerankResult, error) {
	return nil, errors.New("reranker unavailable")
}

func (f *failingBenchReranker) Metadata() ports.RerankerMetadata {
	return ports.RerankerMetadata{Model: "bench", Version: "0"}
}
