package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/projection"
	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

type stubRetriever struct{ candidates []ports.RecallCandidate }

func (s *stubRetriever) RecallWithMeta(context.Context, string, string, string, recall.RecallRequest) ([]ports.RecallCandidate, recall.RecallMeta, error) {
	return s.candidates, recall.RecallMeta{}, nil
}

// TestProjectInterpretsOnlyDeclaredScenarioSignals proves the projection use
// case keeps the historical focus decision when no scenario is declared, selects
// divergence when the caller reports a safety-shaped situation, and rejects
// values that are outside the contract.
func TestProjectInterpretsOnlyDeclaredScenarioSignals(t *testing.T) {
	projector := &Projector{
		Orchestrator: projection.NewOrchestrator(projection.Policy{
			AllowDivergenceHint:      true,
			AllowWeakCandidateRecall: true,
			MaxCandidateBudget:       100,
			MaxRankingBudget:         100,
			MaxInjectionTokenBudget:  8192,
			MaxRelationDepth:         4,
		}),
		Pipeline: projection.NewPipeline(projection.PipelinePolicy{}, &stubRetriever{}, nil, nil),
	}
	cases := []struct {
		name    string
		payload string
		want    projection.Mode
		wantErr bool
	}{
		{"no scenario keeps focus", `{"query":"q"}`, projection.ModeFocus, false},
		{"clear task keeps focus", `{"query":"q","scenario":{"clarity":"clear","progress_percent":80}}`, projection.ModeFocus, false},
		{"repeated failures diverge", `{"query":"q","scenario":{"repeated_failures":2}}`, projection.ModeDivergence, false},
		{"conflicting evidence diverges", `{"query":"q","scenario":{"conflict_count":1}}`, projection.ModeDivergence, false},
		{"evidence gap diverges", `{"query":"q","scenario":{"evidence_gap_count":1}}`, projection.ModeDivergence, false},
		{"unclear and stalled diverges", `{"query":"q","scenario":{"clarity":"unclear","progress_percent":10}}`, projection.ModeDivergence, false},
		{"unknown clarity is rejected", `{"query":"q","scenario":{"clarity":"confused"}}`, "", true},
		{"progress above range is rejected", `{"query":"q","scenario":{"progress_percent":140}}`, "", true},
		{"negative failures is rejected", `{"query":"q","scenario":{"repeated_failures":-1}}`, "", true},
		{"negative conflict count is rejected", `{"query":"q","scenario":{"conflict_count":-2}}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := dto.Envelope{
				Version: dto.CurrentVersion, RequestID: "req-1", IdempotencyKey: "idem-1",
				Principal: dto.Principal{Type: "user", ID: "user-1"},
				Scope:     dto.Scope{TenantID: "tenant-1", UserID: "user-1", Type: "session", SessionID: "session-1"},
				Privacy:   dto.Privacy{Visibility: "private"},
				Payload:   json.RawMessage(tc.payload),
			}
			result, err := projector.Project(context.Background(), agentaccess.Scoped{TenantID: "tenant-1", UserID: "user-1", ScopeType: "session", SessionID: "session-1"}, envelope)
			if tc.wantErr {
				if !errors.Is(err, dto.ErrInvalidEnvelope) {
					t.Fatalf("error = %v, want invalid envelope", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			proj, ok := result.(projection.Projection)
			if !ok {
				t.Fatalf("result = %T", result)
			}
			if proj.Metadata.Mode != string(tc.want) {
				t.Fatalf("mode = %q, want %q (reason %q)", proj.Metadata.Mode, tc.want, proj.Metadata.Reason)
			}
		})
	}
}

// TestProjectScenarioSignalsCannotWidenBudget proves scenario signals are not a
// way to exceed policy limits.
func TestProjectScenarioSignalsCannotWidenBudget(t *testing.T) {
	projector := &Projector{
		Orchestrator: projection.NewOrchestrator(projection.Policy{
			AllowDivergenceHint:      true,
			AllowWeakCandidateRecall: true,
			MaxCandidateBudget:       10,
			MaxRankingBudget:         10,
			MaxInjectionTokenBudget:  100,
			MaxRelationDepth:         2,
		}),
		Pipeline: projection.NewPipeline(projection.PipelinePolicy{}, &stubRetriever{}, nil, nil),
	}
	envelope := dto.Envelope{
		Version: dto.CurrentVersion, RequestID: "req-1", IdempotencyKey: "idem-1",
		Principal: dto.Principal{Type: "user", ID: "user-1"},
		Scope:     dto.Scope{TenantID: "tenant-1", UserID: "user-1", Type: "session", SessionID: "session-1"},
		Privacy:   dto.Privacy{Visibility: "private"},
		Budget:    dto.Budget{Candidates: 10000, Ranking: 10000, InjectionTokens: 1000000},
		Payload:   json.RawMessage(`{"query":"q","scenario":{"repeated_failures":5}}`),
	}
	result, err := projector.Project(context.Background(), agentaccess.Scoped{TenantID: "tenant-1", UserID: "user-1", ScopeType: "session", SessionID: "session-1"}, envelope)
	if err != nil {
		t.Fatal(err)
	}
	proj := result.(projection.Projection)
	if proj.Metadata.Mode != string(projection.ModeDivergence) {
		t.Fatalf("mode = %q, want divergence", proj.Metadata.Mode)
	}
	if proj.Usage.TokensInjected > 100 {
		t.Fatalf("injected tokens = %d, want them capped by policy", proj.Usage.TokensInjected)
	}
}
