package recall

import (
	"context"
	"errors"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

type failingEmbedder struct {
	metadata ports.EmbeddingMetadata
	err      error
}

func (f failingEmbedder) Metadata() ports.EmbeddingMetadata                { return f.metadata }
func (f failingEmbedder) Embed(context.Context, string) ([]float32, error) { return nil, f.err }

type successEmbedder struct{ metadata ports.EmbeddingMetadata }

func (f successEmbedder) Metadata() ports.EmbeddingMetadata { return f.metadata }
func (f successEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0, 0}, nil
}

type failingVectorRepository struct{ err error }

func (f failingVectorRepository) SaveEmbedding(context.Context, string, string, string, string, string, []float32) error {
	return f.err
}
func (f failingVectorRepository) SearchSimilar(context.Context, string, string, string, string, string, string, []float32, int) ([]ports.VectorSearchResult, error) {
	return nil, f.err
}

func TestEmbeddingFailureKeepsStructuredAndFullTextRecall(t *testing.T) {
	search := &fakeSearchRepository{
		byType:  map[string][]ports.MemorySearchResult{"fact": {{Node: node("structured", "fact", "", "structured"), Score: .1}}},
		byQuery: map[string][]ports.MemorySearchResult{"query": {{Node: node("text", "fact", "", "text hit"), Score: .7}}},
	}
	embedder := failingEmbedder{metadata: ports.EmbeddingMetadata{ModelID: "model", Artifact: "model.gguf", Version: "v1"}, err: errors.New("local model timed out")}
	service := NewService(search, &fakeRelationRepository{}, &fakeMemoryRepository{}, &fakeEmbeddingRepository{}, embedder)
	candidates, meta, err := service.RecallWithMeta(context.Background(), "tenant", "user", "session", RecallRequest{Query: "query", MemoryType: "fact"})
	if err != nil {
		t.Fatalf("RecallWithMeta(): %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%d, want structured + lexical", len(candidates))
	}
	if len(meta.Degraded) != 1 || meta.Degraded[0] == "" {
		t.Fatalf("degraded metadata=%v", meta.Degraded)
	}
	if !containsString(meta.ChannelsUsed, ports.RecallChannelFullText) || !containsString(meta.ChannelsUsed, ports.RecallChannelStructured) {
		t.Fatalf("channels used=%v", meta.ChannelsUsed)
	}
}

func TestVectorIndexFailureFallsBackToLexicalRecall(t *testing.T) {
	search := &fakeSearchRepository{byQuery: map[string][]ports.MemorySearchResult{"query": {{Node: node("text", "fact", "", "text hit"), Score: .7}}}}
	embedder := successEmbedder{metadata: ports.EmbeddingMetadata{ModelID: "model", Artifact: "model.gguf", Version: "v1", Dimensions: 4}}
	vectorErr := errors.New("pgvector index unavailable")
	service := NewService(search, &fakeRelationRepository{}, &fakeMemoryRepository{}, failingVectorRepository{err: vectorErr}, embedder)
	candidates, meta, err := service.RecallWithMeta(context.Background(), "tenant", "user", "session", RecallRequest{Query: "query"})
	if err != nil {
		t.Fatalf("RecallWithMeta(): %v", err)
	}
	if len(candidates) != 1 || candidates[0].Node.ID != "text" {
		t.Fatalf("lexical results=%#v", candidates)
	}
	if len(meta.Degraded) != 1 || !containsString(meta.Degraded, "vector-index-unavailable: pgvector index unavailable") {
		t.Fatalf("degraded=%v", meta.Degraded)
	}
}

func TestRerankerFailureFallsBackToExplainableRules(t *testing.T) {
	first := rankedNode("rule-first", nil)
	first.Node.ContentText = "a"
	second := rankedNode("rule-second", nil)
	second.Node.ContentText = "b"
	candidates := []RankedCandidate{{RecallCandidate: first, FinalScore: .8, Included: true}, {RecallCandidate: second, FinalScore: .4, Included: true}}
	result, meta := RankWithFallback(context.Background(), &fakeReranker{err: errors.New("reranker offline")}, "query", candidates)
	if meta.Mode != "explainable-rules" || meta.DegradedReason != "reranker-failed: rerank candidates: reranker offline" {
		t.Fatalf("meta=%#v", meta)
	}
	if result[0].Node.ID != "rule-first" || result[0].FinalScore != .8 {
		t.Fatalf("fallback changed deterministic order: %#v", result)
	}
}

func TestMissingRerankerUsesExplainableRuleFallback(t *testing.T) {
	candidate := RankedCandidate{RecallCandidate: rankedNode("m1", nil), FinalScore: .7, Included: true}
	result, meta := RankWithFallback(context.Background(), nil, "query", []RankedCandidate{candidate})
	if len(result) != 1 || meta.Mode != "explainable-rules" || meta.DegradedReason != "reranker-unavailable" {
		t.Fatalf("result/meta=%#v %#v", result, meta)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestVectorChannelDropsLooseSimilarity(t *testing.T) {
	search := &fakeSearchRepository{byQuery: map[string][]ports.MemorySearchResult{"query": {}}}
	embedder := successEmbedder{metadata: ports.EmbeddingMetadata{ModelID: "model", Artifact: "model.gguf", Version: "v1", Dimensions: 4}}
	vectors := &fakeEmbeddingRepository{similar: map[string][]ports.VectorSearchResult{"v1": {
		{Node: node("close", "fact", "", "close neighbour"), Distance: 1 - DefaultVectorSimilarityFloor - 0.05},
		{Node: node("loose", "fact", "", "loose neighbour"), Distance: 1 - DefaultVectorSimilarityFloor + 0.05},
	}}}
	service := NewService(search, &fakeRelationRepository{}, &fakeMemoryRepository{}, vectors, embedder)
	candidates, meta, err := service.RecallWithMeta(context.Background(), "tenant", "user", "session", RecallRequest{Query: "query"})
	if err != nil {
		t.Fatalf("RecallWithMeta(): %v", err)
	}
	if len(candidates) != 1 || candidates[0].Node.ID != "close" {
		t.Fatalf("candidates=%#v, want only the close neighbour", candidates)
	}
	if !containsString(meta.ChannelsUsed, ports.RecallChannelVector) {
		t.Fatalf("channels=%v, want vector channel recorded", meta.ChannelsUsed)
	}
	if len(meta.Degraded) != 0 {
		t.Fatalf("degraded=%v, want none", meta.Degraded)
	}
}
