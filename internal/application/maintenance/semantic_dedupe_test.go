package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func node(id, text string) ports.MemoryNodeRecord {
	return ports.MemoryNodeRecord{ID: id, ScopeType: "user-global", ScopeID: "u1", ContentText: text}
}

func TestGroupDuplicatesExactAndSemantic(t *testing.T) {
	nodes := []ports.MemoryNodeRecord{node("a", "alpha"), node("b", "beta"), node("c", "delta"), node("d", "delta")}
	// a and b are paraphrases (semantic edge); c is an exact copy of d.
	adjacency := map[string][]string{"a": {"b"}}
	groups := groupDuplicates(nodes, adjacency)
	if groups["a"] != groups["b"] {
		t.Fatalf("semantic neighbors did not group: %#v", groups)
	}
	if groups["c"] != groups["d"] {
		t.Fatalf("exact duplicates did not group: %#v", groups)
	}
	if distinctGroups(groups) != 2 {
		t.Fatalf("groups = %#v, want 2 distinct", groups)
	}
}

func TestGroupDuplicatesIsTransitiveAndDeterministic(t *testing.T) {
	nodes := []ports.MemoryNodeRecord{node("a", "1"), node("b", "2"), node("c", "3")}
	adjacency := map[string][]string{"a": {"b"}, "b": {"c"}}
	groups := groupDuplicates(nodes, adjacency)
	if groups["a"] != groups["b"] || groups["b"] != groups["c"] {
		t.Fatalf("transitive grouping failed: %#v", groups)
	}
	if groups["a"] != "a" {
		t.Fatalf("group id = %q, want smallest member id a", groups["a"])
	}
}

type fakeNeighbors struct{ edges map[string][]string }

func (f fakeNeighbors) ListSemanticNeighbors(_ context.Context, _ string, query ports.SemanticNeighborQuery) ([]ports.SemanticNeighbor, error) {
	var out []ports.SemanticNeighbor
	for _, id := range f.edges[query.ReferenceMemoryID] {
		out = append(out, ports.SemanticNeighbor{Node: ports.MemoryNodeRecord{ID: id}, Similarity: 0.95})
	}
	return out, nil
}

func (f fakeNeighbors) EmbeddingReady(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestDedupeJobCountsSemanticDuplicates(t *testing.T) {
	runs := &fakeRuns{}
	job := &DedupeJob{
		PlanName: "dedupe", Runs: runs,
		Reader:    fakeCandidates{nodes: []ports.MemoryNodeRecord{node("a", "alpha"), node("b", "beta"), node("c", "gamma")}},
		Neighbors: fakeNeighbors{edges: map[string][]string{"a": {"b"}}},
		Threshold: 0.9,
		NewID:     func() (string, error) { return "id", nil },
		Clock:     func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runs.finished) != 1 || runs.finished[0].DiscardedCount != 1 || runs.finished[0].CandidateCount != 2 {
		t.Fatalf("semantic dedupe run = %#v", runs.finished)
	}
}

func TestGroupDuplicatesKeepsScopesSeparate(t *testing.T) {
	nodes := []ports.MemoryNodeRecord{
		{ID: "a", ScopeType: "user-global", ScopeID: "u1", ContentText: "same"},
		{ID: "b", ScopeType: "user-global", ScopeID: "u2", ContentText: "same"},
	}
	groups := groupDuplicates(nodes, nil)
	if groups["a"] == groups["b"] {
		t.Fatalf("cross-scope nodes grouped: %#v", groups)
	}
}

func TestTriageJobGroupsSemanticDuplicates(t *testing.T) {
	nodes := []ports.MemoryNodeRecord{
		{ID: "m1", UserID: "user-1", ScopeType: "user-global", ScopeID: "user-1", ContentText: "prefers dark mode"},
		{ID: "m2", UserID: "user-1", ScopeType: "user-global", ScopeID: "user-1", ContentText: "likes a dark theme"},
	}
	triage := &fakeTriage{}
	job := &TriageJob{
		PlanName:  "triage",
		Reader:    fakeCandidates{nodes: nodes},
		Neighbors: fakeNeighbors{edges: map[string][]string{"m1": {"m2"}}},
		Threshold: 0.9,
		Evidence:  fakeEvidence{},
		Triage:    triage,
		Runs:      &fakeRuns{},
		NewID:     func() (string, error) { return "id", nil },
		Clock:     func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(triage.records) != 2 {
		t.Fatalf("triage records = %d, want 2", len(triage.records))
	}
	if triage.records[0].DuplicateGroup != triage.records[1].DuplicateGroup {
		t.Fatalf("semantic duplicates not grouped: %#v", triage.records)
	}
}
