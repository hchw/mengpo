package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/ports"
)

func testBatch(task string) ports.AnalysisBatch {
	return ports.AnalysisBatch{
		TenantID:      "tenant-a",
		RunID:         "run-1",
		TaskType:      task,
		PromptVersion: PromptVersion,
		SchemaVersion: "schema-v1",
		Events: []ports.AnalysisEvent{{
			EventID:    "evt-1",
			SessionID:  "sess-1",
			OccurredAt: time.Now().UTC(),
			Payload:    json.RawMessage(`{"text":"hello","token":"super-secret-token"}`),
		}},
	}
}

func completion(content string) string {
	envelope := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}}
	encoded, _ := json.Marshal(envelope)
	return string(encoded)
}

func TestAnalyzeSuccess(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		content := `{"classifications":[{"event_id":"evt-1","category":"session_event","confidence":0.5}]}`
		_, _ = w.Write([]byte(completion(content)))
	}))
	defer server.Close()

	adapter, err := New(Options{BaseURL: server.URL, Model: "test-model", APIKey: "key-123", AllowExternal: true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := adapter.Analyze(context.Background(), testBatch(string(TaskClassifyEvent)))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(result.Classifications) != 1 || result.Classifications[0].EventID != "evt-1" {
		t.Fatalf("unexpected result %#v", result)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer key-123" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, "test-model") {
		t.Fatalf("request body missing model: %s", gotBody)
	}
}

func TestAnalyzeRejectsEvidenceNotInBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := `{"candidates":[{"candidate_id":"c1","evidence_event_ids":["missing"],"scope_type":"session","scope_id":"sess-1","content":{"text":"x"},"confidence":0.5}]}`
		_, _ = w.Write([]byte(completion(content)))
	}))
	defer server.Close()
	adapter, _ := New(Options{BaseURL: server.URL, Model: "m", AllowExternal: true, MaxAttempts: 1})
	if _, err := adapter.Analyze(context.Background(), testBatch(string(TaskConsolidate))); err == nil {
		t.Fatal("expected validation failure for unknown evidence id")
	}
}

func TestAnalyzeRetriesOnServerError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()
	adapter, _ := New(Options{BaseURL: server.URL, Model: "m", AllowExternal: true, MaxAttempts: 2})
	if _, err := adapter.Analyze(context.Background(), testBatch(string(TaskClassifyEvent))); err == nil {
		t.Fatal("expected error on persistent 5xx")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 attempts", got)
	}
}

func TestAnalyzeMalformedJSONAndOversizedResponse(t *testing.T) {
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer malformed.Close()
	adapter, _ := New(Options{BaseURL: malformed.URL, Model: "m", AllowExternal: true, MaxAttempts: 1})
	if _, err := adapter.Analyze(context.Background(), testBatch(string(TaskClassifyEvent))); !errors.Is(err, ErrLLMBadResponse) {
		t.Fatalf("malformed envelope error = %v, want ErrLLMBadResponse", err)
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxResponseBytes+1024)))
	}))
	defer oversized.Close()
	adapter, _ = New(Options{BaseURL: oversized.URL, Model: "m", AllowExternal: true, MaxAttempts: 1})
	if _, err := adapter.Analyze(context.Background(), testBatch(string(TaskClassifyEvent))); err == nil {
		t.Fatal("expected oversized response to fail")
	}
}

func TestTaskTypeDispatch(t *testing.T) {
	for _, task := range AllTaskTypes() {
		var body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			_, _ = w.Write([]byte(completion(`{}`)))
		}))
		adapter, _ := New(Options{BaseURL: server.URL, Model: "m", AllowExternal: true, MaxAttempts: 1})
		if _, err := adapter.Analyze(context.Background(), testBatch(string(task))); err != nil {
			t.Fatalf("Analyze(%s) error = %v", task, err)
		}
		if !strings.Contains(body, taskInstructions[task]) {
			t.Fatalf("prompt for %s missing instruction", task)
		}
		if PromptVersionFor(task) != string(task)+"-"+PromptVersion {
			t.Fatalf("prompt version for %s = %q", task, PromptVersionFor(task))
		}
		server.Close()
	}
	if _, err := ParseTaskType("not_a_task"); err == nil {
		t.Fatal("expected unknown task type to fail")
	}
}

func TestExternalAnalysisDisabledSendsNothing(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(completion(`{}`)))
	}))
	defer server.Close()
	adapter, err := New(Options{BaseURL: server.URL, Model: "m", AllowExternal: false})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := adapter.Analyze(context.Background(), testBatch(string(TaskClassifyEvent))); !errors.Is(err, ErrExternalAnalysisDisabled) {
		t.Fatalf("error = %v, want ErrExternalAnalysisDisabled", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("adapter performed an outbound request while disabled")
	}
}

func TestPrivacyRedactionAppliedBeforeRequest(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(completion(`{"classifications":[{"event_id":"evt-1","category":"c","confidence":0.5}]}`)))
	}))
	defer server.Close()
	adapter, _ := New(Options{BaseURL: server.URL, Model: "m", AllowExternal: true})
	service := analysis.NewWithPrivacy(analysis.Providers{Analyst: adapter}, analysis.PrivacyPolicy{RejectSensitive: false})
	if _, err := service.Analyze(context.Background(), testBatch(string(TaskClassifyEvent))); err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("sensitive token leaked into the outbound request")
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatal("expected redacted field marker in outbound request")
	}
}
