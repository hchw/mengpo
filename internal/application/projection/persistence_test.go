package projection

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeProjectionRepository struct {
	event ports.ProjectionEvent
	cache map[string]ports.ProjectionCacheEntry
}

func (f *fakeProjectionRepository) RecordProjection(_ context.Context, _ string, event ports.ProjectionEvent) error {
	f.event = event
	return nil
}
func (f *fakeProjectionRepository) FindProjection(_ context.Context, _, _ string) (ports.ProjectionLookup, error) {
	return ports.ProjectionLookup{}, ports.ErrProjectionNotFound
}

func (f *fakeProjectionRepository) GetProjectionCache(_ context.Context, _, key, _, _, _ string) (json.RawMessage, bool, error) {
	entry, ok := f.cache[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		return nil, false, nil
	}
	return entry.Response, true, nil
}
func (f *fakeProjectionRepository) PutProjectionCache(_ context.Context, _ string, entry ports.ProjectionCacheEntry) error {
	if f.cache == nil {
		f.cache = map[string]ports.ProjectionCacheEntry{}
	}
	f.cache[entry.CacheKey] = entry
	return nil
}

func TestPersistenceRecordsSelectionExclusionBudgetAndProvenance(t *testing.T) {
	repo := &fakeProjectionRepository{}
	persistence := NewPersistence(repo)
	candidate := recall.RankedCandidate{RecallCandidate: ports.RecallCandidate{Node: ports.MemoryNodeRecord{ID: "00000000-0000-4000-8000-000000000701"}, Channels: []string{ports.RecallChannelFullText, ports.RecallChannelVector}}, FinalScore: .83, RerankerScore: floatPtr(.95), Included: true, Reason: recall.RankReason{Relevance: .8, Confidence: .9, Coverage: .66, Freshness: .7, RerankerScore: floatPtr(.95), Channels: []string{ports.RecallChannelFullText, ports.RecallChannelVector}}}
	blocked := recall.RankedCandidate{RecallCandidate: ports.RecallCandidate{Node: ports.MemoryNodeRecord{ID: "00000000-0000-4000-8000-000000000702"}, Channels: []string{ports.RecallChannelVector}}, Included: false, ExcludedReason: "cross-session"}
	budgetResult := BudgetResult{Selected: []Item{{Candidate: candidate, Text: "selected", TokenCost: 8}}, Excluded: []Item{{Candidate: blocked, ExcludedReason: "cross-session"}}, Usage: BudgetUsage{CandidatesSeen: 2, CandidatesRanked: 1, CandidatesInjected: 1, TokensInjected: 8}}
	decision := Decision{Mode: ModeFocus, Reason: "task clear", Scope: CandidateScope{CandidateLimit: 10, RankingLimit: 5, InjectionTokenBudget: 100}}
	if projectionID, err := persistence.Record(context.Background(), "tenant", "req-1", "user-1", "", decision, []recall.RankedCandidate{candidate, blocked}, budgetResult, "reranker-unavailable"); err != nil {
		t.Fatal(err)
	} else if projectionID == "" || projectionID != repo.event.ID {
		t.Fatalf("Record() identifier = %q, want it to name the stored event %q", projectionID, repo.event.ID)
	}
	if repo.event.ID == "" || repo.event.RequestID != "req-1" || repo.event.Mode != "focus" || repo.event.DegradedMode != "reranker-unavailable" {
		t.Fatalf("event metadata=%#v", repo.event)
	}
	if len(repo.event.SelectedIDs) != 1 || repo.event.SelectedIDs[0] != candidate.Node.ID {
		t.Fatalf("selected ids=%v", repo.event.SelectedIDs)
	}
	var selection map[string]map[string]any
	if err := json.Unmarshal(repo.event.SelectionReasons, &selection); err != nil {
		t.Fatal(err)
	}
	if selection[candidate.Node.ID]["score"] != .83 || selection[candidate.Node.ID]["reranker_score"] != .95 {
		t.Fatalf("selection explanation=%v", selection)
	}
	var excluded map[string]map[string]any
	if err := json.Unmarshal(repo.event.ExcludedReasons, &excluded); err != nil {
		t.Fatal(err)
	}
	if excluded[blocked.Node.ID]["reason"] != "cross-session" {
		t.Fatalf("excluded explanation=%v", excluded)
	}
	var budgetDoc map[string]any
	if err := json.Unmarshal(repo.event.Budget, &budgetDoc); err != nil {
		t.Fatal(err)
	}
	if budgetDoc["usage"] == nil || budgetDoc["limits"] == nil {
		t.Fatalf("budget not persisted: %s", repo.event.Budget)
	}
	var provenance map[string][]string
	if err := json.Unmarshal(repo.event.Provenance, &provenance); err != nil {
		t.Fatal(err)
	}
	if len(provenance[candidate.Node.ID]) != 2 {
		t.Fatalf("provenance=%v", provenance)
	}
}

func TestProjectionCacheKeyBindsTenantScopeModelAndBudget(t *testing.T) {
	budget := Budget{CandidateLimit: 10, RankingLimit: 5, InjectionTokenLimit: 100}
	base := CacheKey("tenant-a", "user", "session", "session", "queryhash", "focus", "model-v1", budget)
	cases := []string{
		CacheKey("tenant-b", "user", "session", "session", "queryhash", "focus", "model-v1", budget),
		CacheKey("tenant-a", "user", "session-2", "session", "queryhash", "focus", "model-v1", budget),
		CacheKey("tenant-a", "user", "session", "session", "queryhash", "focus", "model-v2", budget),
		CacheKey("tenant-a", "user", "session", "session", "queryhash", "diverge", "model-v1", budget),
		CacheKey("tenant-a", "user", "session", "session", "queryhash", "focus", "model-v1", Budget{CandidateLimit: 9, RankingLimit: 5, InjectionTokenLimit: 100}),
	}
	for _, value := range cases {
		if value == base {
			t.Fatal("cache key failed to vary across tenant/scope/mode/model/budget")
		}
	}
}

func TestPersistenceCacheValidatesTTLAndRoundTrips(t *testing.T) {
	repo := &fakeProjectionRepository{}
	persistence := NewPersistence(repo)
	key := CacheKey("tenant", "user", "", "user-global", "hash", "focus", "models", Budget{CandidateLimit: 2, RankingLimit: 2, InjectionTokenLimit: 100})
	if err := persistence.PutCache(context.Background(), "tenant", key, "user", "", "user-global", json.RawMessage(`{"text":"context"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	value, ok, err := persistence.GetCache(context.Background(), "tenant", key, "user", "", "user-global")
	if err != nil || !ok || string(value) != `{"text":"context"}` {
		t.Fatalf("cache result=%s ok=%v err=%v", value, ok, err)
	}
	if err := persistence.PutCache(context.Background(), "tenant", key, "user", "", "user-global", json.RawMessage(`{}`), 0); err == nil {
		t.Fatal("non-positive cache TTL accepted")
	}
	otherKey := CacheKey("tenant", "user", "", "user-global", "other-query", "focus", "models", Budget{CandidateLimit: 2, RankingLimit: 2, InjectionTokenLimit: 100})
	if _, ok, err := persistence.GetCache(context.Background(), "tenant", otherKey, "user", "", "user-global"); err != nil || ok {
		t.Fatalf("cache miss=%v err=%v", ok, err)
	}
}

func floatPtr(value float64) *float64 { return &value }
