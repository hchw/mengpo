package reranker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/ports"
)

func TestHTTPRerankContract(t *testing.T) {
	var seenAuth, seenModel string
	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("request path = %q, want /rerank", r.URL.Path)
		}
		seenAuth = r.Header.Get("Authorization")
		var payload struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		seenModel, seenQuery = payload.Model, payload.Query
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 1, "relevance_score": 0.93},
				{"index": 0, "relevance_score": 0.41},
			},
		})
	}))
	defer server.Close()

	adapter, err := NewHTTP(config.RerankerConfig{Enabled: true, BaseURL: server.URL, Model: "bge-reranker", APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewHTTP(): %v", err)
	}
	if adapter.Metadata().Model != "bge-reranker" {
		t.Fatalf("metadata model = %q", adapter.Metadata().Model)
	}
	results, err := adapter.Rank(context.Background(), "租户隔离方案", []ports.RerankCandidate{
		{ID: "m1", Text: "结构化检索路径"},
		{ID: "m2", Text: "租户schema隔离的实现"},
	})
	if err != nil {
		t.Fatalf("Rank(): %v", err)
	}
	if seenAuth != "Bearer secret" || seenModel != "bge-reranker" || seenQuery != "租户隔离方案" {
		t.Fatalf("request missing identity: auth=%q model=%q query=%q", seenAuth, seenModel, seenQuery)
	}
	if len(results) != 2 || results[0].ID != "m2" || results[0].Relevance != 0.93 || results[1].ID != "m1" || results[1].Relevance != 0.41 {
		t.Fatalf("results = %#v", results)
	}
}

func TestHTTPRerankRejectsBadProviders(t *testing.T) {
	ctx := context.Background()
	if _, err := NewHTTP(config.RerankerConfig{Enabled: false}); !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disabled config error = %v", err)
	}
	if _, err := NewHTTP(config.RerankerConfig{Enabled: true, BaseURL: "", Model: ""}); err == nil {
		t.Fatal("missing base URL/model accepted")
	}
	adapter, err := NewHTTP(config.RerankerConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Rank(ctx, "", []ports.RerankCandidate{{ID: "m1", Text: "x"}}); err == nil {
		t.Fatal("empty query accepted")
	}
	if _, err := adapter.Rank(ctx, "q", nil); err == nil {
		t.Fatal("empty candidate list accepted")
	}
	if _, err := adapter.Rank(ctx, "q", []ports.RerankCandidate{{ID: "m1", Text: " "}}); err == nil {
		t.Fatal("blank candidate text accepted")
	}

	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	adapter, err = NewHTTP(config.RerankerConfig{Enabled: true, BaseURL: unavailable.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Rank(ctx, "q", []ports.RerankCandidate{{ID: "m1", Text: "x"}}); err == nil {
		t.Fatal("provider 503 accepted")
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"index":7,"relevance_score":0.9}]}`))
	}))
	defer malformed.Close()
	adapter, err = NewHTTP(config.RerankerConfig{Enabled: true, BaseURL: malformed.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Rank(ctx, "q", []ports.RerankCandidate{{ID: "m1", Text: "x"}}); err == nil {
		t.Fatal("out-of-range result index accepted")
	}

	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer partial.Close()
	adapter, err = NewHTTP(config.RerankerConfig{Enabled: true, BaseURL: partial.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Rank(ctx, "q", []ports.RerankCandidate{{ID: "m1", Text: "x"}}); err == nil {
		t.Fatal("missing result for a document accepted")
	}
}
