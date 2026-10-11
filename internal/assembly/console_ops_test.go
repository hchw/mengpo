package assembly

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/providerconfig"
	"github.com/hchw/mengpo/internal/platform/providercrypto"
	"github.com/hchw/mengpo/internal/ports"
)

type opsStore struct {
	records map[string]ports.ProviderConfigRecord
}

func (f *opsStore) Get(_ context.Context, tenantID, provider string) (ports.ProviderConfigRecord, bool, error) {
	if f.records == nil {
		return ports.ProviderConfigRecord{}, false, nil
	}
	record, ok := f.records[tenantID+"|"+provider]
	return record, ok, nil
}

func (f *opsStore) Upsert(_ context.Context, tenantID string, record ports.ProviderConfigRecord) error {
	if f.records == nil {
		f.records = map[string]ports.ProviderConfigRecord{}
	}
	f.records[tenantID+"|"+record.Provider] = record
	return nil
}

func (f *opsStore) Delete(_ context.Context, tenantID, provider string) error {
	delete(f.records, tenantID+"|"+provider)
	return nil
}

type opsBuilder struct{}

func (opsBuilder) Build(cfg providerconfig.EffectiveConfig) (ports.MemoryAnalyst, error) {
	return analysis.RuleFallback{}, nil
}

func (opsBuilder) Probe(context.Context, providerconfig.EffectiveConfig) (providerconfig.ProbeResult, error) {
	return providerconfig.ProbeResult{OK: true, Latency: 5 * time.Millisecond}, nil
}

type opsSchedules struct {
	upserts []struct {
		tenant, name string
		cadence      time.Duration
		enabled      bool
	}
}

func (f *opsSchedules) Upsert(_ context.Context, tenantID, name string, cadence time.Duration, enabled bool) error {
	f.upserts = append(f.upserts, struct {
		tenant, name string
		cadence      time.Duration
		enabled      bool
	}{tenantID, name, cadence, enabled})
	return nil
}

type opsRuns struct{}

func (opsRuns) StartAnalysisRun(context.Context, string, ports.AnalysisRunRecord) (bool, error) {
	return true, nil
}
func (opsRuns) FinishAnalysisRun(context.Context, string, string, ports.AnalysisRunUpdate) (bool, error) {
	return true, nil
}
func (opsRuns) LinkRunOutputs(context.Context, string, string, []string) (bool, error) {
	return true, nil
}
func (opsRuns) ListAnalysisRuns(context.Context, string, ports.AnalysisRunFilter) ([]ports.AnalysisRunView, error) {
	return []ports.AnalysisRunView{{AnalysisRunRecord: ports.AnalysisRunRecord{
		ID: "run-1", TaskType: "consolidate_memory", Trigger: ports.TriggerSchedule, Provider: "memory-llm",
		Model: "m", PromptVersion: "llm-v1", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}, CandidateCount: 2, LatencyMS: 12}}, nil
}

type opsRunDetails struct{}

func (opsRunDetails) GetAnalysisRunDetail(_ context.Context, _, runID string, _ int) (ports.AnalysisRunDetail, bool, error) {
	if runID != "run-1" {
		return ports.AnalysisRunDetail{}, false, nil
	}
	return ports.AnalysisRunDetail{
		Run: ports.AnalysisRunView{AnalysisRunRecord: ports.AnalysisRunRecord{
			ID: "run-1", TaskType: "analyze_failure", Trigger: ports.TriggerEvent, Status: "succeeded",
			InputEventIDs: []string{"ev-1"}, CreatedAt: time.Unix(0, 0).UTC(),
		}, OutputMemoryIDs: []string{"mem-1"}, DegradedReason: "primary_timeout"},
		Inputs:  []ports.AnalysisRunInput{{ID: "ev-1", SourceType: "tool", MessageType: "tool.failure", Payload: `{"stderr":"boom"}`, OccurredAt: time.Unix(0, 0).UTC()}},
		Outputs: []ports.AnalysisRunOutput{{ID: "mem-1", Status: "candidate", MemoryType: "insight", ScopeType: "user-global", Confidence: 0.6, ContentText: "boom on retry", CreatedAt: time.Unix(0, 0).UTC()}},
	}, true, nil
}

type opsAudit struct{ events []ports.AuditEventRecord }

func (f *opsAudit) RecordAuditEvent(_ context.Context, _ string, event ports.AuditEventRecord) error {
	f.events = append(f.events, event)
	return nil
}

func opsHandler(t *testing.T, roles []string, store *opsStore, schedules *opsSchedules, audit *opsAudit) http.Handler {
	t.Helper()
	sealer, err := providercrypto.New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	service := &providerconfig.Service{Store: store, Sealer: sealer, Router: analysis.NewRouter(analysis.RuleFallback{}), Builder: opsBuilder{}, Provider: "memory-llm"}
	ops := &ConsoleOps{
		Providers: service,
		Schedules: schedules,
		ScheduleStatus: func(context.Context, string) ([]ports.ScheduleStatus, error) {
			return []ports.ScheduleStatus{{Name: "consolidate", Cadence: 24 * time.Hour, LastStatus: "ok"}}, nil
		},
		Runs:       opsRuns{},
		RunDetails: opsRunDetails{},
		Audit:      audit,
	}
	mux := http.NewServeMux()
	ops.Register(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := agentaccess.Identity{TenantID: "tenant-a", UserID: "user-1", SourceID: "user-1", Roles: roles, Source: agentaccess.SourceVerifiedUser}
		mux.ServeHTTP(w, r.WithContext(agentaccess.WithIdentity(r.Context(), identity)))
	})
}

func opsRequest(t *testing.T, handler http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

func TestProviderEndpointsNeverExposeSecret(t *testing.T) {
	store := &opsStore{}
	schedules := &opsSchedules{}
	audit := &opsAudit{}
	handler := opsHandler(t, []string{"owner"}, store, schedules, audit)

	status, _ := opsRequest(t, handler, http.MethodPut, "/api/v1/providers", consoleEnvelope("tenant-a", "user-1", "req-1", map[string]any{
		"enabled": true, "base_url": "https://llm.example", "model": "m", "api_key": "sk-live-secret",
	}))
	if status != http.StatusOK {
		t.Fatalf("PUT providers status = %d", status)
	}
	// The stored record must be ciphertext, and the response must not contain it.
	stored := store.records["tenant-a|memory-llm"]
	if string(stored.SecretCiphertext) == "sk-live-secret" || len(stored.SecretCiphertext) == 0 {
		t.Fatalf("stored secret is not encrypted: %+v", stored)
	}
	status, body := opsRequest(t, handler, http.MethodPost, "/api/v1/providers", consoleEnvelope("tenant-a", "user-1", "req-2", map[string]any{}))
	if status != http.StatusOK {
		t.Fatalf("POST providers status = %d", status)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "sk-live-secret") {
		t.Fatalf("provider view leaked the secret: %s", encoded)
	}
	data := body["data"].(map[string]any)
	if data["has_secret"] != true || data["source"] != "tenant" {
		t.Fatalf("provider view = %#v", data)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "provider_config.update" || audit.events[0].ActorID != "user-1" {
		t.Fatalf("audit events = %#v", audit.events)
	}
	changes := string(audit.events[0].Changes)
	if strings.Contains(changes, "sk-live-secret") {
		t.Fatalf("audit leaked the secret: %s", changes)
	}
}

func TestProviderTestEndpointDoesNotPersist(t *testing.T) {
	store := &opsStore{}
	handler := opsHandler(t, []string{"owner"}, store, &opsSchedules{}, &opsAudit{})
	status, body := opsRequest(t, handler, http.MethodPost, "/api/v1/providers/test", consoleEnvelope("tenant-a", "user-1", "req-t", map[string]any{
		"enabled": true, "base_url": "https://llm.example", "model": "m",
	}))
	if status != http.StatusOK {
		t.Fatalf("providers/test status = %d", status)
	}
	data := body["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("providers/test data = %#v", data)
	}
	if len(store.records) != 0 {
		t.Fatal("connectivity test persisted configuration")
	}
}

func TestProviderWritesRequireAdmin(t *testing.T) {
	store := &opsStore{}
	handler := opsHandler(t, []string{"member"}, store, &opsSchedules{}, &opsAudit{})
	status, _ := opsRequest(t, handler, http.MethodPut, "/api/v1/providers", consoleEnvelope("tenant-a", "user-1", "req-1", map[string]any{
		"enabled": true, "base_url": "https://llm.example", "model": "m",
	}))
	if status != http.StatusForbidden {
		t.Fatalf("member PUT providers status = %d, want 403", status)
	}
	if len(store.records) != 0 {
		t.Fatal("non-admin write persisted a record")
	}
	// Reads remain available to members.
	status, _ = opsRequest(t, handler, http.MethodPost, "/api/v1/providers", consoleEnvelope("tenant-a", "user-1", "req-2", map[string]any{}))
	if status != http.StatusOK {
		t.Fatalf("member POST providers status = %d, want 200", status)
	}
}

func TestScheduleAndAnalysisRunEndpoints(t *testing.T) {
	schedules := &opsSchedules{}
	handler := opsHandler(t, []string{"owner"}, &opsStore{}, schedules, &opsAudit{})

	status, body := opsRequest(t, handler, http.MethodPost, "/api/v1/schedules", consoleEnvelope("tenant-a", "user-1", "req-1", map[string]any{}))
	if status != http.StatusOK {
		t.Fatalf("POST schedules status = %d", status)
	}
	items := body["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("schedule items = %#v", items)
	}

	status, _ = opsRequest(t, handler, http.MethodPut, "/api/v1/schedules", consoleEnvelope("tenant-a", "user-1", "req-2", map[string]any{
		"name": "consolidate", "cadence_seconds": 3600, "enabled": true,
	}))
	if status != http.StatusOK || len(schedules.upserts) != 1 || schedules.upserts[0].tenant != "tenant-a" {
		t.Fatalf("schedule update = %#v status=%d", schedules.upserts, status)
	}

	status, body = opsRequest(t, handler, http.MethodPost, "/api/v1/analysis-runs", consoleEnvelope("tenant-a", "user-1", "req-3", map[string]any{}))
	if status != http.StatusOK {
		t.Fatalf("POST analysis-runs status = %d", status)
	}
	runs := body["data"].(map[string]any)["items"].([]any)
	if len(runs) != 1 {
		t.Fatalf("analysis runs = %#v", runs)
	}
	run := runs[0].(map[string]any)
	if run["task_type"] != "consolidate_memory" || run["candidate_count"].(float64) != 2 {
		t.Fatalf("analysis run = %#v", run)
	}
}

func TestAnalysisRunDetailEndpoint(t *testing.T) {
	handler := opsHandler(t, []string{"owner"}, &opsStore{}, &opsSchedules{}, &opsAudit{})

	status, body := opsRequest(t, handler, http.MethodPost, "/api/v1/analysis-runs/detail", consoleEnvelope("tenant-a", "user-1", "req-d1", map[string]any{"id": "run-1"}))
	if status != http.StatusOK {
		t.Fatalf("detail status = %d body = %#v", status, body)
	}
	data := body["data"].(map[string]any)
	if data["run"].(map[string]any)["task_type"] != "analyze_failure" {
		t.Fatalf("detail run = %#v", data["run"])
	}
	inputs := data["inputs"].([]any)
	outputs := data["outputs"].([]any)
	if len(inputs) != 1 || inputs[0].(map[string]any)["message_type"] != "tool.failure" {
		t.Fatalf("detail inputs = %#v", inputs)
	}
	if len(outputs) != 1 || outputs[0].(map[string]any)["content_text"] != "boom on retry" {
		t.Fatalf("detail outputs = %#v", outputs)
	}

	status, _ = opsRequest(t, handler, http.MethodPost, "/api/v1/analysis-runs/detail", consoleEnvelope("tenant-a", "user-1", "req-d2", map[string]any{"id": "missing"}))
	if status != http.StatusNotFound {
		t.Fatalf("missing detail status = %d", status)
	}
	status, _ = opsRequest(t, handler, http.MethodPost, "/api/v1/analysis-runs/detail", consoleEnvelope("tenant-a", "user-1", "req-d3", map[string]any{}))
	if status != http.StatusBadRequest {
		t.Fatalf("missing id status = %d", status)
	}
}
