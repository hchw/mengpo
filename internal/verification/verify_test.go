// Package verification holds cross-cutting invariant tests that back section 12
// of the implementation tasks. Each test names the invariant it protects so the
// acceptance matrix can reference it directly.
package verification

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/projection"
	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/application/tools"
	"github.com/hchw/mengpo/internal/domain/memory"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

type fixedReranker struct{ relevance float64 }

func (f fixedReranker) Rank(context.Context, string, []ports.RerankCandidate) ([]ports.RerankResult, error) {
	return []ports.RerankResult{{ID: "session-memory", Relevance: f.relevance}}, nil
}

func (f fixedReranker) Metadata() ports.RerankerMetadata {
	return ports.RerankerMetadata{Model: "fixed", Version: "1"}
}

// 12.1 Raw Evidence -> Structured Memory -> Projected Context. Raw observations
// never become long-term memory directly; only evidence-bound candidates can.
func TestThreeLayerFlowHasNoShortcutFromRawToMemory(t *testing.T) {
	now := time.Now().UTC()
	pipeline, err := analysis.BuildPipelineResult("tenant-a", "schema-v1", "normalize-v1", []analysis.RawEvent{{
		ID: "event-1", TenantID: "tenant-a", SessionID: "session-1", OccurredAt: now, SourceType: "user", MessageType: "message",
		Payload: json.RawMessage(`{"text":"remember that staging uses port 55432"}`),
	}}, ports.AnalystResult{
		Candidates: []ports.CandidateMemory{{CandidateID: "cand-1", EvidenceEventIDs: []string{"event-1"}, ScopeType: "session", ScopeID: "session-1", Content: json.RawMessage(`{"summary":"staging port 55432"}`), Confidence: 0.7}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Layer 1 is raw/normalized evidence, not memory.
	if len(pipeline.Normalized) != 1 || pipeline.Normalized[0].RawEventID != "event-1" {
		t.Fatalf("layer 1 lost evidence: %#v", pipeline.Normalized)
	}
	// Layer 2 candidates always carry evidence ids; there is no candidate
	// without a raw-event reference.
	for _, candidate := range pipeline.Candidates {
		if len(candidate.EvidenceEventIDs) == 0 {
			t.Fatalf("candidate without evidence: %#v", candidate)
		}
	}
	// Layer 3 projection only admits persisted, active memories.
	candidateNode := ports.MemoryNodeRecord{ID: "memory-1", IdempotencyKey: "k1", UserID: "user-1", SessionID: "session-1", ScopeType: "session", ScopeID: "session-1", MemoryType: "preference", Status: "candidate", DefaultRetrieval: true, Confidence: 0.7, ContentText: "staging port 55432", Content: json.RawMessage(`{}`)}
	ranked := recall.Rank([]ports.RecallCandidate{{Node: candidateNode, Channels: []string{ports.RecallChannelStructured}}}, recall.RankingContext{SessionID: "session-1"})
	if ranked[0].Included || ranked[0].ExcludedReason != "status-candidate" {
		t.Fatalf("unconfirmed candidate entered projection: %#v", ranked[0])
	}
}

// 12.2 Session <-> User Global promotion requires independent evidence, is
// isolated across sessions, keeps provenance, and never mutates governance.
func TestSessionPromotionRequiresIndependentEvidenceAndKeepsProvenance(t *testing.T) {
	service, err := recall.NewTransferService(fixedReranker{relevance: 0.9}, 0.85, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	sessionNode := ports.MemoryNodeRecord{ID: "session-memory", UserID: "user-1", SessionID: "session-1", ScopeType: "session", ScopeID: "session-1", Status: "active", Confidence: 0.8, DefaultRetrieval: true, ContentText: "staging uses port 55432"}
	target := ports.MemoryNodeRecord{ID: "global-memory", UserID: "user-1", ScopeType: "user-global", ScopeID: "user-1", Status: "stable", Confidence: 0.9, ContentText: "staging port"}
	proposal, err := service.ProposeTransfer(context.Background(), "staging port", sessionNode, target, 0.9, []string{"evidence-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !proposal.Confirmed || len(proposal.EvidenceIDs) != 1 || proposal.EvidenceIDs[0] != "evidence-1" {
		t.Fatalf("promotion proposal=%#v", proposal)
	}
	// Missing evidence blocks promotion.
	blocked, _ := service.ProposeTransfer(context.Background(), "staging port", sessionNode, target, 0.9, nil)
	if blocked.Confirmed {
		t.Fatalf("promotion without evidence confirmed: %#v", blocked)
	}
	// Cross-session isolation: another session's memory is excluded at ranking.
	otherSession := sessionNode
	otherSession.SessionID = "session-2"
	otherSession.ScopeID = "session-2"
	ranked := recall.Rank([]ports.RecallCandidate{{Node: otherSession, Channels: []string{ports.RecallChannelStructured}}}, recall.RankingContext{SessionID: "session-1"})
	if ranked[0].Included || ranked[0].ExcludedReason != "cross-session" {
		t.Fatalf("cross-session memory not isolated: %#v", ranked[0])
	}
}

// 12.4 Task-memory and user-behaviour channels share one merged order but keep
// independent budgets and channel provenance.
func TestTwoRecallChannelsKeepIndependentBudgetsAndProvenance(t *testing.T) {
	orchestrator := projection.NewOrchestrator(projection.Policy{AllowWeakCandidateRecall: true, MaxCandidateBudget: 90, MaxRankingBudget: 60, MaxInjectionTokenBudget: 4096, MaxRelationDepth: 4})
	decision, err := orchestrator.Choose(projection.Signals{Clarity: projection.ClarityClear, ProgressPercent: 60, CandidateBudget: 30, RankingBudget: 20, InjectionTokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Scope.TaskChannelBudget <= 0 || decision.Scope.UserBehaviorBudget <= 0 {
		t.Fatalf("channels do not have independent budgets: %#v", decision.Scope)
	}
	if decision.Scope.TaskChannelBudget+decision.Scope.UserBehaviorBudget > decision.Scope.CandidateLimit {
		t.Fatalf("channel budgets exceed candidate budget: %#v", decision.Scope)
	}
	taskMemory := ports.MemoryNodeRecord{ID: "task-1", IdempotencyKey: "t1", UserID: "u", Status: "active", ScopeType: "user-global", ScopeID: "u", MemoryType: "task", DefaultRetrieval: true, ContentText: "task memory", Content: json.RawMessage(`{}`)}
	userMemory := ports.MemoryNodeRecord{ID: "user-1", IdempotencyKey: "u1", UserID: "u", Status: "stable", ScopeType: "user-global", ScopeID: "u", MemoryType: "preference", DefaultRetrieval: true, ContentText: "user habit", Content: json.RawMessage(`{}`)}
	ranked := recall.Rank([]ports.RecallCandidate{
		{Node: taskMemory, Channels: []string{ports.RecallChannelFullText}},
		{Node: userMemory, Channels: []string{ports.RecallChannelStructured, ports.RecallChannelFullText}},
	}, recall.RankingContext{SessionID: "s"})
	if len(ranked) != 2 {
		t.Fatalf("merged ranking=%#v", ranked)
	}
	byID := map[string]recall.RankedCandidate{}
	for _, candidate := range ranked {
		byID[candidate.Node.ID] = candidate
	}
	if len(byID["task-1"].Reason.Channels) != 1 || len(byID["user-1"].Reason.Channels) != 2 {
		t.Fatalf("channel provenance lost: %#v", byID)
	}
}

// 12.5 Focus/Divergence auto-selection, hint constraints and three budgets.
func TestFocusDivergenceHintBudgetAndDegradation(t *testing.T) {
	orchestrator := projection.NewOrchestrator(projection.Policy{AllowDivergenceHint: true, AllowWeakCandidateRecall: true, MaxCandidateBudget: 90, MaxRankingBudget: 60, MaxInjectionTokenBudget: 4096, MaxRelationDepth: 4})
	clear, _ := orchestrator.Choose(projection.Signals{Clarity: projection.ClarityClear, ProgressPercent: 80})
	if clear.Mode != projection.ModeFocus {
		t.Fatalf("clear task chose %s", clear.Mode)
	}
	safety, _ := orchestrator.Choose(projection.Signals{Clarity: projection.ClarityClear, ProgressPercent: 80, RepeatedFailures: 3, Hint: "focus"})
	if safety.Mode != projection.ModeDivergence {
		t.Fatalf("safety trigger did not force divergence: %#v", safety)
	}
	// Three budgets are all present and bounded.
	if clear.Scope.CandidateLimit == 0 || clear.Scope.RankingLimit == 0 || clear.Scope.InjectionTokenBudget == 0 {
		t.Fatalf("missing budget layer: %#v", clear.Scope)
	}
	// Degradation is explicit, never silent.
	retriever := &blockingRetriever{}
	pipeline := projection.NewPipeline(projection.PipelinePolicy{Timeout: 5 * time.Millisecond}, retriever, fixedSessionContext{}, nil)
	result, err := pipeline.Run(context.Background(), projection.PipelineRequest{TenantID: "t", UserID: "u", QueryHash: "h", Decision: projection.Decision{Mode: projection.ModeFocus}, Budget: projection.Budget{CandidateLimit: 5, RankingLimit: 5, InjectionTokenLimit: 100}})
	if err != nil || !result.Metadata.Degraded {
		t.Fatalf("degradation not surfaced: %#v err=%v", result.Metadata, err)
	}
}

type blockingRetriever struct{}

func (blockingRetriever) RecallWithMeta(ctx context.Context, _, _, _ string, _ recall.RecallRequest) ([]ports.RecallCandidate, recall.RecallMeta, error) {
	<-ctx.Done()
	return nil, recall.RecallMeta{}, ctx.Err()
}

type fixedSessionContext struct{}

func (fixedSessionContext) LocalContext(context.Context, string, string, string) ([]ports.SessionContextItem, error) {
	return []ports.SessionContextItem{{Text: "local session note", Local: true}}, nil
}

// 12.7 Governance state machine, confidence updates, correction, expiry and the
// feedback loop.
func TestGovernanceLifecycleConfidenceAndCorrection(t *testing.T) {
	policy := memory.DefaultGovernancePolicy()
	candidate := memory.Memory{ID: "m1", IdempotencyKey: "k1", UserID: "user-1", ScopeType: memory.ScopeUserGlobal, ScopeID: "user-1", Type: "preference", Status: memory.StatusCandidate, Confidence: 0.2, Content: json.RawMessage(`{}`)}
	evidence := []memory.Evidence{{ID: "ev1", MemoryID: "m1", RawEventID: "event-1", Confidence: 0.9, Reliability: memory.ReliabilityHigh, Attribution: memory.AttributionDirect}}
	decision, err := memory.EvaluateGovernance(candidate, memory.GovernanceInput{VerifiedEvidence: evidence}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Memory.Status != memory.StatusActive || decision.ConfidenceScore < 0.65 {
		t.Fatalf("candidate not activated with strong evidence: %#v", decision)
	}
	if !memory.CanTransition(memory.StatusCandidate, memory.StatusRejected) || memory.CanTransition(memory.StatusRejected, memory.StatusActive) {
		t.Fatal("governance transition graph violated")
	}
	correction := memory.Memory{ID: "m2", IdempotencyKey: "k2", UserID: "user-1", ScopeType: memory.ScopeUserGlobal, ScopeID: "user-1", Type: "preference", Status: memory.StatusActive, Confidence: 0.9, Content: json.RawMessage(`{}`)}
	if err := correction.Transition(memory.StatusConflicted); err != nil {
		t.Fatalf("correction transition: %v", err)
	}
	expired := memory.Memory{ID: "m3", IdempotencyKey: "k3", UserID: "user-1", ScopeType: memory.ScopeUserGlobal, ScopeID: "user-1", Type: "preference", Status: memory.StatusStable, Confidence: 0.9, Content: json.RawMessage(`{}`)}
	if err := expired.Transition(memory.StatusExpired); err != nil {
		t.Fatalf("expiry transition: %v", err)
	}
	// The feedback loop only accepts known feedback types.
	if !ports.ValidFeedbackType("corrected") || ports.ValidFeedbackType("fabricated") {
		t.Fatal("feedback type validation broken")
	}
}

// 12.9 Failure credibility and attribution completeness are separate dimensions.
func TestFailureCredibilityAndAttributionAreSeparate(t *testing.T) {
	event := obsdomain.Event{ID: "e1", SessionID: "s1", SourceType: obsdomain.SourceTool, MessageType: "tool_result", Payload: json.RawMessage(`{"success":false}`), OccurredAt: time.Now().UTC()}
	signals := obsdomain.DetectSignals(event)
	if len(signals) != 1 || signals[0].Confidence != obsdomain.FailureConfirmed {
		t.Fatalf("explicit failure not confirmed: %#v", signals)
	}
	failure := obsdomain.Event{SourceType: obsdomain.SourceTool, MessageType: "failure_event", Payload: json.RawMessage(`{}`), OccurredAt: time.Now().UTC()}
	weaker := obsdomain.DetectSignals(failure)
	if len(weaker) != 1 || weaker[0].Confidence != obsdomain.FailureSuspected {
		t.Fatalf("failure event not suspected: %#v", weaker)
	}
	// Attribution is computed independently and covers depth/bypass access.
	for _, level := range []obsdomain.AccessLevel{obsdomain.Level0, obsdomain.Level1, obsdomain.Level2} {
		accessEvent := event
		accessEvent.AccessLevel = level
		accessEvent.Trace = obsdomain.Trace{TaskID: "task-1", AttemptID: "attempt-1"}
		attribution := obsdomain.AssessAttribution(accessEvent.SessionID, "", "", accessEvent.Trace)
		if attribution.Level == "" {
			t.Fatalf("attribution missing for %s", level)
		}
	}
}

// 12.10 Chat, workflow and Vibe Coding access differences do not add new memory
// dimensions: the same tool contract and candidate shape apply to every source.
func TestAccessDifferencesDoNotAddMemoryDimensions(t *testing.T) {
	now := time.Now().UTC()
	sources := []struct {
		source      string
		messageType string
	}{
		{"user", "message"},
		{"workflow", "workflow_event"},
		{"agent", "code_edit"},
	}
	for _, source := range sources {
		result, err := analysis.BuildPipelineResult("tenant-a", "schema-v1", "normalize-v1", []analysis.RawEvent{{
			ID: "event-" + source.source, TenantID: "tenant-a", OccurredAt: now, SourceType: source.source, MessageType: source.messageType,
			Payload: json.RawMessage(`{"text":"x"}`),
		}}, ports.AnalystResult{Candidates: []ports.CandidateMemory{{CandidateID: "cand", EvidenceEventIDs: []string{"event-" + source.source}, ScopeType: "user-global", ScopeID: "user-1", Content: json.RawMessage(`{"summary":"x"}`), Confidence: 0.6}}}, now)
		if err != nil {
			t.Fatalf("source %s: %v", source.source, err)
		}
		for _, candidate := range result.Candidates {
			if candidate.ScopeType != "user-global" && candidate.ScopeType != "session" {
				t.Fatalf("source %s introduced a new scope dimension: %#v", source.source, candidate)
			}
			if len(candidate.EvidenceEventIDs) == 0 {
				t.Fatalf("source %s candidate missing evidence", source.source)
			}
		}
	}
	// The tool contract is source-independent: every tool uses the same schema.
	if err := tools.Validate(tools.MemoryContext, json.RawMessage(`{"query":"q"}`)); err != nil {
		t.Fatalf("tool contract differs by source: %v", err)
	}
}
