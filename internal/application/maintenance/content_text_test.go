package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

type fakeContentTextStore struct {
	nodes   []ports.MemoryNodeRecord
	updated map[string]string
}

func (f *fakeContentTextStore) ListMissingContentText(_ context.Context, _ string, _ int) ([]ports.MemoryNodeRecord, error) {
	return f.nodes, nil
}

func (f *fakeContentTextStore) RefreshContentText(_ context.Context, _, memoryID, contentText string) (bool, error) {
	if f.updated == nil {
		f.updated = map[string]string{}
	}
	if _, exists := f.updated[memoryID]; exists {
		return false, nil
	}
	f.updated[memoryID] = contentText
	return true, nil
}

type fakeRebuilder struct{ calls int }

func (f *fakeRebuilder) RebuildForModel(context.Context, string) (int, error) {
	f.calls++
	return 1, nil
}

func TestContentTextBackfillRendersStructuredContent(t *testing.T) {
	store := &fakeContentTextStore{nodes: []ports.MemoryNodeRecord{
		{ID: "m1", Content: json.RawMessage(`{"name":"log-trace-mcp","type":"project_overview"}`)},
		{ID: "m2", Content: json.RawMessage(`{}`)},
	}}
	rebuilder := &fakeRebuilder{}
	job := &ContentTextBackfillJob{PlanName: "content-text-backfill", Store: store, Embeddings: rebuilder, BatchSize: 10}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.updated["m1"] == "" {
		t.Fatal("structured content must be projected")
	}
	if _, ok := store.updated["m2"]; ok {
		t.Fatal("empty content must not be projected")
	}
	if rebuilder.calls != 1 {
		t.Fatalf("rebuilder calls = %d, want 1", rebuilder.calls)
	}
}

func TestContentTextBackfillSkipsEmbeddingWhenNothingRendered(t *testing.T) {
	store := &fakeContentTextStore{nodes: []ports.MemoryNodeRecord{
		{ID: "m1", Content: json.RawMessage(`{}`)},
		{ID: "m2", Content: json.RawMessage(`[]`)},
	}}
	rebuilder := &fakeRebuilder{}
	job := &ContentTextBackfillJob{PlanName: "content-text-backfill", Store: store, Embeddings: rebuilder, BatchSize: 10}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(store.updated) != 0 {
		t.Fatalf("empty content must stay empty, got %#v", store.updated)
	}
	if rebuilder.calls != 0 {
		t.Fatalf("rebuilder calls = %d, want 0", rebuilder.calls)
	}
}
