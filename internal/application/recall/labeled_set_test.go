package recall

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

// Labeled retrieval set: every entry names the memories a query must recall
// and the channel expected to surface each one. The suite verifies candidate
// coverage — the union of the five recall channels must contain every labeled
// relevant memory.
type labeledQuery struct {
	name     string
	request  RecallRequest
	query    string
	expected map[string]string // memory id -> expected channel
}

type fakeSearchRepository struct {
	byType  map[string][]ports.MemorySearchResult
	byQuery map[string][]ports.MemorySearchResult
}

func (f *fakeSearchRepository) Search(ctx context.Context, tenantID, userID, sessionID string, request ports.MemorySearchRequest) (ports.MemorySearchPage, error) {
	items := f.byQuery[request.Query]
	if request.MemoryType != "" {
		items = f.byType[request.MemoryType]
	}
	page := ports.MemorySearchPage{Items: items, Total: int64(len(items)), Page: 1, PageSize: len(items)}
	if request.PageSize > 0 && len(items) > request.PageSize {
		page.Items = items[:request.PageSize]
		page.Total = int64(len(items))
	}
	return page, nil
}

type fakeRelationRepository struct {
	related map[string][]ports.RelatedMemory
}

func (f *fakeRelationRepository) ListRelated(ctx context.Context, tenantID string, memoryIDs []string, limit int) ([]ports.RelatedMemory, error) {
	results := make([]ports.RelatedMemory, 0)
	for _, id := range memoryIDs {
		results = append(results, f.related[id]...)
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

type fakeMemoryRepository struct {
	nodes map[string]ports.MemoryNodeRecord
}

func (f *fakeMemoryRepository) Create(ctx context.Context, tenantID string, node ports.MemoryNodeRecord) (ports.MemoryNodeRecord, error) {
	return node, nil
}
func (f *fakeMemoryRepository) Get(ctx context.Context, tenantID, nodeID string) (ports.MemoryNodeRecord, error) {
	if node, ok := f.nodes[nodeID]; ok {
		return node, nil
	}
	return ports.MemoryNodeRecord{}, ports.ErrMemoryNotFound
}
func (f *fakeMemoryRepository) Update(ctx context.Context, tenantID string, node ports.MemoryNodeRecord, expectedVersion int64) (ports.MemoryNodeRecord, error) {
	return node, nil
}
func (f *fakeMemoryRepository) LoadScopeTree(ctx context.Context, tenantID, userID, sessionID string, maxParentDepth, limit int) (ports.MemoryScopeTree, error) {
	return ports.MemoryScopeTree{}, nil
}

type fakeEmbeddingRepository struct {
	similar map[string][]ports.VectorSearchResult
}

func (f *fakeEmbeddingRepository) SaveEmbedding(ctx context.Context, tenantID, memoryID, modelID, artifact, version string, vector []float32) error {
	return nil
}
func (f *fakeEmbeddingRepository) SearchSimilar(ctx context.Context, tenantID, userID, sessionID, modelID, artifact, version string, vector []float32, limit int) ([]ports.VectorSearchResult, error) {
	results := f.similar[version]
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func node(id, memoryType, parentID, text string) ports.MemoryNodeRecord {
	return ports.MemoryNodeRecord{ID: id, MemoryType: memoryType, ParentID: parentID, ContentText: text, Status: "stable"}
}

func TestLabeledRetrievalSetCoversAllChannels(t *testing.T) {
	const (
		structuredHit = "00000000-0000-4000-8000-000000000601"
		fullTextHit   = "00000000-0000-4000-8000-000000000602"
		vectorHit     = "00000000-0000-4000-8000-000000000603"
		relationHit   = "00000000-0000-4000-8000-000000000604"
		parentHit     = "00000000-0000-4000-8000-000000000605"
		noise         = "00000000-0000-4000-8000-000000000699"
	)
	searchRepo := &fakeSearchRepository{
		byType: map[string][]ports.MemorySearchResult{
			"failure": {{Node: node(structuredHit, "failure", "", "结构化通道命中"), Score: 0}},
		},
		byQuery: map[string][]ports.MemorySearchResult{
			"租户隔离": {{Node: node(fullTextHit, "fact", "", "租户隔离的全文命中"), Score: 0.9}},
		},
	}
	relationRepo := &fakeRelationRepository{related: map[string][]ports.RelatedMemory{
		fullTextHit: {{Node: node(relationHit, "fact", "", "关系通道命中"), RelationType: "relates_to", Confidence: 0.8}},
	}}
	memoryRepo := &fakeMemoryRepository{nodes: map[string]ports.MemoryNodeRecord{
		parentHit: node(parentHit, "summary", "", "父节点补全命中"),
	}}
	embeddingRepo := &fakeEmbeddingRepository{similar: map[string][]ports.VectorSearchResult{
		"sha-1": {{Node: node(vectorHit, "fact", parentHit, "向量通道命中"), Distance: 0.25}},
	}}
	service := NewService(searchRepo, relationRepo, memoryRepo, embeddingRepo)
	ctx := context.Background()

	labeled := []labeledQuery{
		{
			name:     "structured only",
			request:  RecallRequest{MemoryType: "failure"},
			expected: map[string]string{structuredHit: ports.RecallChannelStructured},
		},
		{
			name:    "full text",
			request: RecallRequest{Query: "租户隔离"},
			expected: map[string]string{
				fullTextHit: ports.RecallChannelFullText,
				relationHit: ports.RecallChannelRelation,
			},
		},
		{
			name: "vector with parent completion",
			request: RecallRequest{
				Query:             "租户隔离",
				EmbeddingModelID:  "all-MiniLM-L6-v2",
				EmbeddingArtifact: "all-MiniLM-L6-v2-Q8_0.gguf",
				EmbeddingVersion:  "sha-1",
				EmbeddingVector:   make([]float32, 4),
			},
			expected: map[string]string{
				vectorHit: ports.RecallChannelVector,
				parentHit: ports.RecallChannelParent,
			},
		},
	}

	for _, entry := range labeled {
		entry := entry
		t.Run(entry.name, func(t *testing.T) {
			candidates, err := service.Recall(ctx, "tenant", "user", "session", entry.request)
			if err != nil {
				t.Fatalf("Recall(): %v", err)
			}
			channels := map[string]map[string]bool{}
			for _, candidate := range candidates {
				if channels[candidate.Node.ID] == nil {
					channels[candidate.Node.ID] = map[string]bool{}
				}
				for _, channel := range candidate.Channels {
					channels[candidate.Node.ID][channel] = true
				}
			}
			for memoryID, wantChannel := range entry.expected {
				if !channels[memoryID][wantChannel] {
					t.Fatalf("labeled memory %s not recalled via %q; channels = %#v (all candidates: %s)",
						memoryID, wantChannel, channels[memoryID], describeCandidates(candidates))
				}
			}
			if channels[noise] != nil {
				t.Fatalf("noise memory recalled: %s", describeCandidates(candidates))
			}
		})
	}
}

func TestRecallRejectsMissingIdentityAndMergesDuplicateChannels(t *testing.T) {
	service := NewService(&fakeSearchRepository{}, &fakeRelationRepository{}, &fakeMemoryRepository{}, &fakeEmbeddingRepository{})
	if _, err := service.Recall(context.Background(), "", "user", "session", RecallRequest{Query: "x"}); err == nil {
		t.Fatal("Recall accepted an empty tenant id")
	}
	if _, err := service.Recall(context.Background(), "tenant", "", "session", RecallRequest{Query: "x"}); err == nil {
		t.Fatal("Recall accepted an empty user id")
	}
	// Same memory surfaced by both structured and full-text channels keeps both labels.
	searchRepo := &fakeSearchRepository{
		byType:  map[string][]ports.MemorySearchResult{"fact": {{Node: node("m1", "fact", "", "重复命中"), Score: 0.1}}},
		byQuery: map[string][]ports.MemorySearchResult{"重复": {{Node: node("m1", "fact", "", "重复命中"), Score: 0.5}}},
	}
	relationRepo := &fakeRelationRepository{}
	memoryRepo := &fakeMemoryRepository{nodes: map[string]ports.MemoryNodeRecord{}}
	embeddingRepo := &fakeEmbeddingRepository{}
	service = NewService(searchRepo, relationRepo, memoryRepo, embeddingRepo)
	candidates, err := service.Recall(context.Background(), "tenant", "user", "session", RecallRequest{Query: "重复", MemoryType: "fact"})
	if err != nil {
		t.Fatalf("Recall(): %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 deduplicated candidate", len(candidates))
	}
	sort.Strings(candidates[0].Channels)
	if fmt.Sprint(candidates[0].Channels) != "[full-text structured]" {
		t.Fatalf("channels = %v, want [full-text structured]", candidates[0].Channels)
	}
	if candidates[0].Score != 0.5 {
		t.Fatalf("score = %f, want best channel score 0.5", candidates[0].Score)
	}
}

func describeCandidates(candidates []ports.RecallCandidate) string {
	description := ""
	for _, candidate := range candidates {
		description += fmt.Sprintf("\n  %s via %v (score %.3f)", candidate.Node.ID, candidate.Channels, candidate.Score)
	}
	return description
}
