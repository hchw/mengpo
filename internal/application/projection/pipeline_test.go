package projection

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeRetriever struct {
	candidates []ports.RecallCandidate
	meta       recall.RecallMeta
	err        error
	block      time.Duration
}

func (f *fakeRetriever) RecallWithMeta(ctx context.Context, _, _, _ string, _ recall.RecallRequest) ([]ports.RecallCandidate, recall.RecallMeta, error) {
	if f.block > 0 {
		select {
		case <-time.After(f.block):
		case <-ctx.Done():
			return nil, recall.RecallMeta{}, ctx.Err()
		}
	}
	var out []ports.RecallCandidate
	out = append(out, f.candidates...)
	return out, f.meta, f.err
}

type fakeSessionContext struct {
	items []ports.SessionContextItem
	err   error
}

func (f *fakeSessionContext) LocalContext(context.Context, string, string, string) ([]ports.SessionContextItem, error) {
	return f.items, f.err
}

func pipelineCandidate(id, text string, score float64) ports.RecallCandidate {
	return ports.RecallCandidate{Node: ports.MemoryNodeRecord{ID: id, IdempotencyKey: "key-" + id, Status: "active", ScopeType: "user-global", DefaultRetrieval: true, ContentText: text}, Channels: []string{ports.RecallChannelFullText}, Score: score}
}

func pipelineBudget() Budget {
	return Budget{CandidateLimit: 5, RankingLimit: 5, InjectionTokenLimit: 100}
}

func pipelineRequest() PipelineRequest {
	return PipelineRequest{TenantID: "tenant", UserID: "user", SessionID: "session", Query: "q", QueryHash: "hash", Decision: Decision{Mode: ModeFocus, Reason: "clear"}, Budget: pipelineBudget(), ScopeType: "session"}
}

func TestPipelineBuildsProjectionAndRecordsEvent(t *testing.T) {
	repo := &fakeProjectionRepository{}
	pipeline := NewPipeline(PipelinePolicy{RankingContext: recall.RankingContext{SessionID: "session"}}, &fakeRetriever{candidates: []ports.RecallCandidate{pipelineCandidate("a", "alpha", .9)}}, &fakeSessionContext{}, NewPersistence(repo))
	projection, err := pipeline.Run(context.Background(), pipelineRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 1 || projection.Metadata.CacheHit || projection.Metadata.Degraded {
		t.Fatalf("projection=%#v", projection)
	}
	if repo.event.RequestID != "hash" || repo.event.Mode != "focus" {
		t.Fatalf("event not recorded: %#v", repo.event)
	}
}

func TestPipelineDegradesToSessionContextOnTimeout(t *testing.T) {
	session := &fakeSessionContext{items: []ports.SessionContextItem{{MemoryID: "local-1", Text: "current session note", Local: true}}}
	pipeline := NewPipeline(PipelinePolicy{Timeout: 20 * time.Millisecond}, &fakeRetriever{block: time.Second}, session, nil)
	projection, err := pipeline.Run(context.Background(), pipelineRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Metadata.Degraded || !contains(projection.Metadata.DegradedReasons, "retrieval-timeout") {
		t.Fatalf("timeout degradation missing: %#v", projection.Metadata)
	}
	if len(projection.Items) != 1 || projection.Items[0].Text != "current session note" {
		t.Fatalf("session fallback not injected: %#v", projection.Items)
	}
}

func TestPipelineDegradesToSessionContextOnDatabaseError(t *testing.T) {
	pipeline := NewPipeline(PipelinePolicy{DegradeOnDatabaseError: true}, &fakeRetriever{err: errors.New("connection refused")}, &fakeSessionContext{items: []ports.SessionContextItem{{Text: "local"}}}, nil)
	projection, err := pipeline.Run(context.Background(), pipelineRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Metadata.Degraded || len(projection.Items) != 1 {
		t.Fatalf("db degradation=%#v", projection)
	}
	found := false
	for _, reason := range projection.Metadata.DegradedReasons {
		if len(reason) >= len("retrieval-database-unavailable") && reason[:len("retrieval-database-unavailable")] == "retrieval-database-unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("database degradation reason missing: %v", projection.Metadata.DegradedReasons)
	}
}

func TestPipelineSurfacesDatabaseErrorWhenDegradationDisabled(t *testing.T) {
	pipeline := NewPipeline(PipelinePolicy{}, &fakeRetriever{err: errors.New("connection refused")}, &fakeSessionContext{}, nil)
	if _, err := pipeline.Run(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("database error was not surfaced")
	}
}

func TestPipelineSurfacesEmbeddingDegradationWithoutFailing(t *testing.T) {
	repo := &fakeProjectionRepository{}
	pipeline := NewPipeline(PipelinePolicy{RankingContext: recall.RankingContext{SessionID: "session"}}, &fakeRetriever{candidates: []ports.RecallCandidate{pipelineCandidate("a", "alpha", .9)}, meta: recall.RecallMeta{ChannelsUsed: []string{ports.RecallChannelFullText}, Degraded: []string{"embedding-unavailable: offline"}}}, nil, NewPersistence(repo))
	projection, err := pipeline.Run(context.Background(), pipelineRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Metadata.Degraded || !contains(projection.Metadata.DegradedReasons, "embedding-unavailable: offline") {
		t.Fatalf("embedding degradation missing: %#v", projection.Metadata)
	}
	if repo.event.DegradedMode != "degraded" {
		t.Fatalf("projection event did not record degraded mode: %#v", repo.event)
	}
}

func TestPipelineServesCacheHitUntilNewEvidence(t *testing.T) {
	repo := &fakeProjectionRepository{}
	retriever := &fakeRetriever{candidates: []ports.RecallCandidate{pipelineCandidate("a", "alpha", .9)}}
	pipeline := NewPipeline(PipelinePolicy{CacheTTL: time.Minute, RankingContext: recall.RankingContext{SessionID: "session"}}, retriever, nil, NewPersistence(repo))
	first, err := pipeline.Run(context.Background(), pipelineRequest())
	if err != nil || first.Metadata.CacheHit {
		t.Fatalf("first run=%#v err=%v", first.Metadata, err)
	}
	second, err := pipeline.Run(context.Background(), pipelineRequest())
	if err != nil || !second.Metadata.CacheHit || len(second.Items) != 1 {
		t.Fatalf("cache hit not served: %#v err=%v", second.Metadata, err)
	}
	request := pipelineRequest()
	request.NewEvidence = true
	third, err := pipeline.Run(context.Background(), request)
	if err != nil || third.Metadata.CacheHit || !contains(third.Metadata.DegradedReasons, "cache-invalidated: new-evidence") {
		t.Fatalf("new evidence did not bypass cache: %#v err=%v", third.Metadata, err)
	}
}

func TestPipelineSessionFallbackRespectsInjectionBudget(t *testing.T) {
	session := &fakeSessionContext{items: []ports.SessionContextItem{{Text: "0123456789"}, {Text: "abcdefghij"}}}
	pipeline := NewPipeline(PipelinePolicy{Timeout: 10 * time.Millisecond}, &fakeRetriever{block: time.Second}, session, nil)
	request := pipelineRequest()
	request.Budget = Budget{CandidateLimit: 5, RankingLimit: 5, InjectionTokenLimit: 6}
	projection, err := pipeline.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Usage.TokensInjected > 6 || len(projection.Items) == 0 {
		t.Fatalf("session fallback exceeded budget: %#v", projection)
	}
}

func TestPipelineValidatesInputs(t *testing.T) {
	pipeline := NewPipeline(PipelinePolicy{}, &fakeRetriever{}, nil, nil)
	bad := []PipelineRequest{
		{UserID: "u", QueryHash: "h", Budget: pipelineBudget(), Decision: Decision{Mode: ModeFocus}},
		{TenantID: "t", UserID: "u", QueryHash: "h", Budget: pipelineBudget(), Decision: Decision{Mode: "unknown"}},
		{TenantID: "t", UserID: "u", QueryHash: "h", Decision: Decision{Mode: ModeFocus}},
	}
	for _, request := range bad {
		if _, err := pipeline.Run(context.Background(), request); err == nil {
			t.Errorf("Run(%#v) accepted invalid request", request)
		}
	}
	_ = json.Valid
}
