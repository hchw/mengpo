package recall

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func rankedNode(id string, mutate func(*ports.MemoryNodeRecord)) ports.RecallCandidate {
	node := ports.MemoryNodeRecord{
		ID:               id,
		IdempotencyKey:   "key-" + id,
		ScopeType:        "user-global",
		MemoryType:       "fact",
		Status:           "active",
		Confidence:       0.8,
		Applicability:    json.RawMessage(`{}`),
		DefaultRetrieval: true,
		UpdatedAt:        time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	if mutate != nil {
		mutate(&node)
	}
	return ports.RecallCandidate{Node: node, Channels: []string{ports.RecallChannelFullText}, Score: 0.5}
}

func TestRankFiltersScopeApplicabilityAndConflictsWithReasons(t *testing.T) {
	now := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	base := RankingContext{SessionID: "session-1", ContextTags: map[string]string{"language": "go"}, Now: now}
	candidates := []ports.RecallCandidate{
		rankedNode("included", nil),
		rankedNode("cross-session", func(n *ports.MemoryNodeRecord) {
			n.ScopeType = "session"
			n.SessionID = "session-other"
		}),
		rankedNode("own-session", func(n *ports.MemoryNodeRecord) {
			n.ScopeType = "session"
			n.SessionID = "session-1"
		}),
		rankedNode("conflicted", func(n *ports.MemoryNodeRecord) { n.Status = "conflicted" }),
		rankedNode("rejected", func(n *ports.MemoryNodeRecord) { n.Status = "rejected" }),
		rankedNode("candidate-excluded", func(n *ports.MemoryNodeRecord) { n.Status = "candidate" }),
		rankedNode("retrieval-off", func(n *ports.MemoryNodeRecord) { n.DefaultRetrieval = false }),
		rankedNode("applicability-miss", func(n *ports.MemoryNodeRecord) {
			n.Applicability = json.RawMessage(`{"conditions":["python"]}`)
		}),
		rankedNode("applicability-hit", func(n *ports.MemoryNodeRecord) {
			n.Applicability = json.RawMessage(`{"attributes":{"language":"go"}}`)
		}),
		rankedNode("applicability-invalid", func(n *ports.MemoryNodeRecord) {
			n.Applicability = json.RawMessage(`{not-json`)
		}),
		rankedNode("expired", func(n *ports.MemoryNodeRecord) {
			expiry := now.Add(-time.Hour)
			n.ExpiresAt = &expiry
		}),
		rankedNode("deleted", func(n *ports.MemoryNodeRecord) {
			deleted := now.Add(-time.Hour)
			n.DeletedAt = &deleted
		}),
	}
	ranked := Rank(candidates, base)
	byID := map[string]RankedCandidate{}
	for _, item := range ranked {
		byID[item.Node.ID] = item
	}
	wantIncluded := map[string]bool{
		"included": true, "own-session": true, "applicability-hit": true,
	}
	wantExcluded := map[string]string{
		"cross-session":         "cross-session",
		"conflicted":            "status-conflicted",
		"rejected":              "status-rejected",
		"candidate-excluded":    "status-candidate",
		"retrieval-off":         "default-retrieval-off",
		"applicability-miss":    "applicability",
		"applicability-invalid": "applicability-invalid",
		"expired":               "expired",
		"deleted":               "deleted",
	}
	for id := range wantIncluded {
		if !byID[id].Included {
			t.Fatalf("%s should be included, got excluded (%q)", id, byID[id].ExcludedReason)
		}
	}
	for id, reason := range wantExcluded {
		if byID[id].Included {
			t.Fatalf("%s should be excluded", id)
		}
		if byID[id].ExcludedReason != reason {
			t.Fatalf("%s excluded reason = %q, want %q", id, byID[id].ExcludedReason, reason)
		}
	}
	// Explainability: every included candidate carries a full reason breakdown.
	for id := range wantIncluded {
		reason := byID[id].Reason
		if len(reason.Channels) == 0 || reason.Confidence == 0 {
			t.Fatalf("%s missing explainable reason: %#v", id, reason)
		}
	}
	if !byID["applicability-hit"].Reason.ApplicabilityHit {
		t.Fatal("applicability hit not recorded in reason")
	}
	if byID["included"].Reason.ApplicabilityHit {
		t.Fatal("empty applicability recorded as a positive applicability hit")
	}

	// Exploration mode surfaces candidates again.
	exploration := base
	exploration.IncludeCandidates = true
	ranked = Rank(candidates, exploration)
	for _, item := range ranked {
		if item.Node.ID == "candidate-excluded" && !item.Included {
			t.Fatal("candidate status still excluded in exploration mode")
		}
	}
}

func TestRankOrdersByExplainableScoreAndAppliesBudget(t *testing.T) {
	now := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	context := RankingContext{ContextTags: map[string]string{}, Now: now, MaxCandidates: 2}
	strong := rankedNode("strong", nil)
	strong.Score = 0.9
	strong.Channels = []string{ports.RecallChannelFullText, ports.RecallChannelVector, ports.RecallChannelStructured}
	strong.Node.Confidence = 0.95
	strong.Node.UpdatedAt = now
	medium := rankedNode("medium", nil)
	medium.Score = 0.4
	weak := rankedNode("weak", nil)
	weak.Node.Confidence = 0.2
	weak.Node.UpdatedAt = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	ranked := Rank([]ports.RecallCandidate{weak, medium, strong}, context)
	if len(ranked) != 3 {
		t.Fatalf("ranked = %d, want 3 (budget overflow kept with reason)", len(ranked))
	}
	if ranked[0].Node.ID != "strong" {
		t.Fatalf("top candidate = %s, want strong", ranked[0].Node.ID)
	}
	if ranked[1].Node.ID != "medium" {
		t.Fatalf("second candidate = %s, want medium", ranked[1].Node.ID)
	}
	if ranked[2].Included || ranked[2].ExcludedReason != "budget" {
		t.Fatalf("overflow candidate = included:%v reason:%q, want budget exclusion", ranked[2].Included, ranked[2].ExcludedReason)
	}
	if ranked[0].FinalScore <= ranked[1].FinalScore || ranked[1].FinalScore <= ranked[2].FinalScore {
		t.Fatalf("scores not ordered: %v %v %v", ranked[0].FinalScore, ranked[1].FinalScore, ranked[2].FinalScore)
	}
}

func TestRankDeduplicatesByIdempotencyKeyMergingChannels(t *testing.T) {
	first := rankedNode("dup", nil)
	first.Channels = []string{ports.RecallChannelStructured}
	first.Score = 0.2
	second := rankedNode("dup", nil)
	second.Channels = []string{ports.RecallChannelFullText, ports.RecallChannelVector}
	second.Score = 0.7
	ranked := Rank([]ports.RecallCandidate{first, second}, RankingContext{ContextTags: map[string]string{}})
	if len(ranked) != 1 {
		t.Fatalf("ranked = %d, want 1 deduplicated candidate", len(ranked))
	}
	if len(ranked[0].Channels) != 3 {
		t.Fatalf("channels = %v, want merged 3 channels", ranked[0].Channels)
	}
	if ranked[0].Score != 0.7 {
		t.Fatalf("score = %f, want best channel score", ranked[0].Score)
	}
}

func TestFreshnessAndDefaultContexts(t *testing.T) {
	if got := freshness(time.Time{}, time.Now()); got != 1 {
		t.Fatalf("freshness(zero) = %f, want 1", got)
	}
	if got := freshness(time.Now().AddDate(-10, 0, 0), time.Now()); got > 0.02 {
		t.Fatalf("freshness(10y) = %f, want decayed to ~0", got)
	}
	// Ranking without a session id still ranks user-global memories.
	ranked := Rank([]ports.RecallCandidate{rankedNode("global", nil)}, RankingContext{ContextTags: map[string]string{}})
	if len(ranked) != 1 || !ranked[0].Included {
		t.Fatal("user-global memory not ranked without a session id")
	}
}
