package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hchw/mengpo/internal/ports"
)

const (
	DefaultChannelLimit  = 20
	DefaultRelationLimit = 20

	// DefaultVectorSimilarityFloor drops loosely related embeddings from the
	// semantic channel. It is calibrated for the bundled local model
	// (bge-small-zh-v1.5): relevant Chinese query/document pairs land around
	// 0.5-0.7 while unrelated pairs stay below ~0.47, so 0.45 keeps genuinely
	// semantic neighbours without flooding recall with every stored vector.
	DefaultVectorSimilarityFloor = 0.45
)

// RecallRequest describes one candidate recall pass. Query text drives the
// full-text and vector channels; MemoryType drives the structured channel;
// both are optional and independent.
type RecallRequest struct {
	Query      string
	MemoryType string
	// Embedding identity for the vector channel. When empty the vector channel
	// is skipped (structural and full-text recall still work).
	EmbeddingModelID  string
	EmbeddingArtifact string
	EmbeddingVersion  string
	EmbeddingVector   []float32
}

type Service struct {
	search     ports.MemorySearchRepository
	relations  ports.RelationRepository
	memories   ports.MemoryNodeRepository
	embeddings ports.EmbeddingRepository
	embedder   ports.Embedder
}

type RecallMeta struct {
	ChannelsUsed []string
	Degraded     []string
}

func NewService(search ports.MemorySearchRepository, relations ports.RelationRepository, memories ports.MemoryNodeRepository, embeddings ports.EmbeddingRepository, embedders ...ports.Embedder) *Service {
	service := &Service{search: search, relations: relations, memories: memories, embeddings: embeddings}
	if len(embedders) > 0 {
		service.embedder = embedders[0]
	}
	return service
}

// Recall merges five candidate channels: structured (type/status filters),
// full-text, vector, relation expansion of every recalled candidate, and
// parent completion of every recalled candidate. Candidates are keyed by
// memory id; every channel that recalled a node is recorded on it so ranking
// (task 6.4) can explain coverage. No channel is a scope boundary: all
// repositories already enforce tenant, user and session scope.
func (s *Service) Recall(ctx context.Context, tenantID, userID, sessionID string, request RecallRequest) ([]ports.RecallCandidate, error) {
	candidates, _, err := s.RecallWithMeta(ctx, tenantID, userID, sessionID, request)
	return candidates, err
}

func (s *Service) RecallWithMeta(ctx context.Context, tenantID, userID, sessionID string, request RecallRequest) ([]ports.RecallCandidate, RecallMeta, error) {
	meta := RecallMeta{ChannelsUsed: []string{}, Degraded: []string{}}
	if tenantID == "" || userID == "" {
		return nil, meta, errors.New("recall requires tenant and user identity")
	}
	pool := newCandidatePool()

	// Structured channel: type-filtered recall of the active memory base.
	if request.MemoryType != "" {
		page, err := s.search.Search(ctx, tenantID, userID, sessionID, ports.MemorySearchRequest{MemoryType: request.MemoryType, PageSize: DefaultChannelLimit})
		if err != nil {
			return nil, meta, fmt.Errorf("structured recall: %w", err)
		}
		pool.add(page.Items, ports.RecallChannelStructured)
		meta.ChannelsUsed = append(meta.ChannelsUsed, ports.RecallChannelStructured)
	}
	// Full-text channel: lexical match on content_text.
	if request.Query != "" {
		page, err := s.search.Search(ctx, tenantID, userID, sessionID, ports.MemorySearchRequest{Query: request.Query, PageSize: DefaultChannelLimit})
		if err != nil {
			return nil, meta, fmt.Errorf("full-text recall: %w", err)
		}
		pool.add(page.Items, ports.RecallChannelFullText)
		meta.ChannelsUsed = append(meta.ChannelsUsed, ports.RecallChannelFullText)
	}
	// Vector channel: semantic similarity against the caller-provided query
	// vector (already embedded with the same model identity).
	if request.Query != "" {
		vector, modelID, artifact, version := request.EmbeddingVector, request.EmbeddingModelID, request.EmbeddingArtifact, request.EmbeddingVersion
		if len(vector) == 0 && s.embedder != nil {
			metadata := s.embedder.Metadata()
			if modelID == "" || modelID == metadata.ModelID {
				var embedErr error
				if queryEmbedder, ok := s.embedder.(ports.QueryEmbedder); ok {
					vector, embedErr = queryEmbedder.EmbedQuery(ctx, request.Query)
				} else {
					vector, embedErr = s.embedder.Embed(ctx, request.Query)
				}
				if embedErr != nil {
					meta.Degraded = append(meta.Degraded, "embedding-unavailable: "+embedErr.Error())
				}
				modelID, artifact, version = metadata.ModelID, metadata.Artifact, metadata.Version
			} else {
				meta.Degraded = append(meta.Degraded, "embedding-identity-mismatch")
			}
		} else if len(vector) == 0 {
			meta.Degraded = append(meta.Degraded, "embedding-unavailable")
		}
		if len(vector) > 0 && modelID != "" && s.embeddings != nil {
			results, err := s.embeddings.SearchSimilar(ctx, tenantID, userID, sessionID, modelID, artifact, version, vector, DefaultChannelLimit)
			if err != nil {
				meta.Degraded = append(meta.Degraded, "vector-index-unavailable: "+err.Error())
			} else {
				added := 0
				for _, result := range results {
					similarity := 1 - result.Distance
					if similarity < DefaultVectorSimilarityFloor {
						continue
					}
					pool.add([]ports.MemorySearchResult{{Node: result.Node, Score: similarity}}, ports.RecallChannelVector)
					added++
				}
				if added > 0 {
					meta.ChannelsUsed = append(meta.ChannelsUsed, ports.RecallChannelVector)
				}
			}
		}
	}

	// Relation channel: expand every recalled candidate through relations.
	if ids := pool.ids(); len(ids) > 0 && s.relations != nil {
		related, err := s.relations.ListRelated(ctx, tenantID, ids, DefaultRelationLimit)
		if err != nil {
			return nil, meta, fmt.Errorf("relation recall: %w", err)
		}
		for _, item := range related {
			pool.addOne(item.Node, item.Confidence, ports.RecallChannelRelation)
		}
		if len(related) > 0 {
			meta.ChannelsUsed = append(meta.ChannelsUsed, ports.RecallChannelRelation)
		}
	}
	// Parent completion: pull in the parent chain of every candidate so the
	// candidate pool is self-contained for ranking and injection.
	if parentIDs := pool.parentIDs(); len(parentIDs) > 0 && s.memories != nil {
		for _, parentID := range parentIDs {
			parent, err := s.memories.Get(ctx, tenantID, parentID)
			if err != nil {
				if errors.Is(err, ports.ErrMemoryNotFound) {
					continue
				}
				return nil, meta, fmt.Errorf("parent recall: %w", err)
			}
			if parent.DeletedAt != nil {
				continue
			}
			pool.addOne(parent, 0, ports.RecallChannelParent)
		}
		meta.ChannelsUsed = append(meta.ChannelsUsed, ports.RecallChannelParent)
	}

	candidates := pool.candidates()
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Node.UpdatedAt.After(candidates[j].Node.UpdatedAt)
	})
	return candidates, meta, nil
}

type candidatePool struct {
	byID map[string]*ports.RecallCandidate
}

func newCandidatePool() *candidatePool {
	return &candidatePool{byID: map[string]*ports.RecallCandidate{}}
}

func (p *candidatePool) add(items []ports.MemorySearchResult, channel string) {
	for _, item := range items {
		p.addOne(item.Node, item.Score, channel)
	}
}

func (p *candidatePool) addOne(node ports.MemoryNodeRecord, score float64, channel string) {
	if node.ID == "" {
		return
	}
	existing, ok := p.byID[node.ID]
	if !ok {
		existing = &ports.RecallCandidate{Node: node, Score: score, Channels: []string{channel}}
		p.byID[node.ID] = existing
		return
	}
	if score > existing.Score {
		existing.Score = score
	}
	for _, known := range existing.Channels {
		if known == channel {
			return
		}
	}
	existing.Channels = append(existing.Channels, channel)
}

func (p *candidatePool) ids() []string {
	ids := make([]string, 0, len(p.byID))
	for id := range p.byID {
		ids = append(ids, id)
	}
	return ids
}

func (p *candidatePool) parentIDs() []string {
	seen := map[string]bool{}
	var ids []string
	for _, candidate := range p.byID {
		if candidate.Node.ParentID == "" || seen[candidate.Node.ParentID] {
			continue
		}
		if _, alreadyPooled := p.byID[candidate.Node.ParentID]; alreadyPooled {
			continue
		}
		seen[candidate.Node.ParentID] = true
		ids = append(ids, candidate.Node.ParentID)
	}
	return ids
}

func (p *candidatePool) candidates() []ports.RecallCandidate {
	candidates := make([]ports.RecallCandidate, 0, len(p.byID))
	for _, candidate := range p.byID {
		candidates = append(candidates, *candidate)
	}
	return candidates
}
