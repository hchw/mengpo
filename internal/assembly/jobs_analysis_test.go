package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeObservationReader struct{ event observation.Event }

func (f fakeObservationReader) GetObservation(context.Context, string, string) (observation.Event, error) {
	return f.event, nil
}

type fakeNormalizedStore struct {
	stores  int
	marked  int
	lastLen int
}

func (f *fakeNormalizedStore) StoreNormalizedEvents(_ context.Context, _ string, events []ports.NormalizedEventRecord) error {
	f.stores++
	f.lastLen = len(events)
	return nil
}

func (f *fakeNormalizedStore) MarkRawEventProcessed(context.Context, string, string) error {
	f.marked++
	return nil
}

type fakeCandidateWriter struct{ records []ports.CandidateMemoryRecord }

func (f *fakeCandidateWriter) PersistCandidates(_ context.Context, _ string, records []ports.CandidateMemoryRecord) (int, error) {
	f.records = append(f.records, records...)
	return len(records), nil
}

type fakeWorkingMemoryWriter struct{ records []ports.WorkingMemoryRecord }

func (f *fakeWorkingMemoryWriter) PersistWorkingMemory(_ context.Context, _ string, records []ports.WorkingMemoryRecord) (int, error) {
	f.records = append(f.records, records...)
	return len(records), nil
}

type fakeRunStore struct {
	started     []ports.AnalysisRunRecord
	finished    []ports.AnalysisRunUpdate
	finishedIDs []string
	linked      []string
}

func (f *fakeRunStore) StartAnalysisRun(_ context.Context, _ string, record ports.AnalysisRunRecord) (bool, error) {
	f.started = append(f.started, record)
	return true, nil
}

func (f *fakeRunStore) FinishAnalysisRun(_ context.Context, _ string, runID string, update ports.AnalysisRunUpdate) (bool, error) {
	f.finished = append(f.finished, update)
	f.finishedIDs = append(f.finishedIDs, runID)
	return true, nil
}

func (f *fakeRunStore) ListAnalysisRuns(context.Context, string, ports.AnalysisRunFilter) ([]ports.AnalysisRunView, error) {
	return nil, nil
}

func (f *fakeRunStore) LinkRunOutputs(_ context.Context, _, _ string, memoryIDs []string) (bool, error) {
	f.linked = append(f.linked, memoryIDs...)
	return true, nil
}

type fakeSessionOwner struct{ owner string }

func (f fakeSessionOwner) GetSessionOwner(context.Context, string, string) (string, error) {
	return f.owner, nil
}

type countingAnalyst struct {
	calls  int
	result ports.AnalystResult
}

func (c *countingAnalyst) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	c.calls++
	return c.result, nil
}

func dispatcherFor(t *testing.T, event observation.Event, analyst *countingAnalyst, candidates *fakeCandidateWriter, normalized *fakeNormalizedStore, runs *fakeRunStore) *JobDispatcher {
	t.Helper()
	providers := analysis.Providers{}
	if analyst != nil {
		providers.Analyst = analyst
	}
	return &JobDispatcher{
		Observations:         fakeObservationReader{event: event},
		Normalized:           normalized,
		Analysis:             analysis.New(providers),
		Candidates:           candidates,
		WorkingMemory:        &fakeWorkingMemoryWriter{},
		SessionOwner:         fakeSessionOwner{owner: "user-1"},
		Runs:                 runs,
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		PromptVersion:        "llm-v1",
		Provider:             "memory-llm",
		Model:                "test-model",
		NewID:                newUUID,
	}
}

func TestOrdinaryEventNeverCallsModelOrCreatesCandidates(t *testing.T) {
	event := observation.Event{
		ID:          "11111111-1111-4111-8111-111111111111",
		TenantID:    "tenant-a",
		SessionID:   "22222222-2222-4222-8222-222222222222",
		SourceType:  observation.SourceUser,
		MessageType: "message",
		Payload:     json.RawMessage(`{"text":"just chatting"}`),
		OccurredAt:  time.Now().UTC(),
	}
	analyst := &countingAnalyst{}
	candidates := &fakeCandidateWriter{}
	normalized := &fakeNormalizedStore{}
	runs := &fakeRunStore{}
	dispatcher := dispatcherFor(t, event, analyst, candidates, normalized, runs)

	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
		ID: "job-1", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if analyst.calls != 0 {
		t.Fatalf("ordinary event called the model %d times, want 0", analyst.calls)
	}
	if len(candidates.records) != 0 {
		t.Fatalf("ordinary event produced %d candidates, want 0", len(candidates.records))
	}
	if normalized.stores != 1 || normalized.marked != 1 {
		t.Fatalf("normalized stores=%d marked=%d, want 1/1", normalized.stores, normalized.marked)
	}
	if len(runs.started) != 0 {
		t.Fatalf("ordinary event opened %d analysis runs, want 0", len(runs.started))
	}
}

func TestGatedEventPersistsCandidateWithEvidence(t *testing.T) {
	event := observation.Event{
		ID:          "33333333-3333-4333-8333-333333333333",
		TenantID:    "tenant-a",
		SessionID:   "44444444-4444-4444-8444-444444444444",
		SourceType:  observation.SourceUser,
		MessageType: "message",
		Payload:     json.RawMessage(`{"status":"error","text":"migration failed"}`),
		OccurredAt:  time.Now().UTC(),
	}
	analyst := &countingAnalyst{result: ports.AnalystResult{Candidates: []ports.CandidateMemory{{
		CandidateID:      "c1",
		EvidenceEventIDs: []string{event.ID},
		ScopeType:        "user-global",
		ScopeID:          "user-1",
		Content:          json.RawMessage(`{"text":"the migration command fails on retry"}`),
		Confidence:       0.6,
	}}}}
	candidates := &fakeCandidateWriter{}
	normalized := &fakeNormalizedStore{}
	runs := &fakeRunStore{}
	dispatcher := dispatcherFor(t, event, analyst, candidates, normalized, runs)
	audit := &fakeAudit{}
	dispatcher.Audit = audit

	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
		ID: "job-2", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if analyst.calls != 1 {
		t.Fatalf("gated event called the model %d times, want 1", analyst.calls)
	}
	if len(candidates.records) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates.records))
	}
	record := candidates.records[0]
	if record.Node.Status != "candidate" {
		t.Fatalf("candidate status = %q, want candidate", record.Node.Status)
	}
	if record.Node.DefaultRetrieval {
		t.Fatal("candidate must not be default-retrievable")
	}
	if record.Node.ScopeType != "user-global" || record.Node.ScopeID != "user-1" || record.Node.UserID != "user-1" {
		t.Fatalf("candidate scope = %+v", record.Node)
	}
	if len(record.Evidence) != 1 || record.Evidence[0].RawEventID != event.ID {
		t.Fatalf("candidate evidence = %#v", record.Evidence)
	}
	if len(record.Node.Provenance) == 0 {
		t.Fatal("candidate must carry provenance")
	}
	var provenance map[string]any
	if err := json.Unmarshal(record.Node.Provenance, &provenance); err != nil {
		t.Fatalf("decode provenance: %v", err)
	}
	if provenance["model"] != "test-model" || provenance["prompt_version"] != "llm-v1" {
		t.Fatalf("candidate provenance missing model/prompt version: %#v", provenance)
	}
	if len(runs.started) != 1 {
		t.Fatalf("gated event opened %d analysis runs, want 1", len(runs.started))
	}
	if runs.started[0].Model != "test-model" || runs.started[0].PromptVersion != "llm-v1" || runs.started[0].TaskType != observation.TaskFailureAnalysis {
		t.Fatalf("run record = %+v", runs.started[0])
	}
	if len(audit.events) != 1 || audit.events[0].Action != "candidate.created" || audit.events[0].ActorID != "memory-llm" {
		t.Fatalf("candidate provenance audit = %#v", audit.events)
	}
	if len(runs.finished) != 1 || runs.finished[0].Status != ports.AnalysisRunSucceeded || runs.finished[0].CandidateCount != 1 {
		t.Fatalf("finished run = %#v", runs.finished)
	}
}

type fakeAnalysisReader struct{ events []ports.PendingAnalysisEvent }

func (f fakeAnalysisReader) ListPendingAnalysisEvents(context.Context, string, int, int) ([]ports.PendingAnalysisEvent, error) {
	return f.events, nil
}

func (f fakeAnalysisReader) LoadAnalysisEventsByRawIDs(context.Context, string, []string) ([]ports.PendingAnalysisEvent, error) {
	return f.events, nil
}

func TestConsolidateJobPersistsCandidatesWithEvidence(t *testing.T) {
	events := []ports.PendingAnalysisEvent{
		{NormalizedID: "n-a", RawEventID: "raw-a", SessionID: "session-1", OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"text":"a"}`)},
		{NormalizedID: "n-b", RawEventID: "raw-b", SessionID: "session-1", OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"text":"b"}`)},
	}
	analyst := &countingAnalyst{result: ports.AnalystResult{Candidates: []ports.CandidateMemory{{
		CandidateID:      "consolidated-1",
		EvidenceEventIDs: []string{"raw-a", "raw-b"},
		ScopeType:        "user-global",
		ScopeID:          "user-1",
		Content:          json.RawMessage(`{"text":"user prefers concise answers"}`),
		Confidence:       0.7,
	}}}}
	candidates := &fakeCandidateWriter{}
	normalized := &fakeNormalizedStore{}
	runs := &fakeRunStore{}
	dispatcher := dispatcherFor(t, observation.Event{}, analyst, candidates, normalized, runs)
	dispatcher.AnalysisReader = fakeAnalysisReader{events: events}
	dispatcher.Candidates = candidates

	payload, _ := json.Marshal(map[string]any{"raw_event_ids": []string{"raw-a", "raw-b"}})
	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
		ID: "job-consolidate-1", TenantID: "tenant-a", JobType: JobConsolidate, Payload: payload,
	}); err != nil {
		t.Fatalf("Handle(consolidate) error = %v", err)
	}
	if analyst.calls != 1 {
		t.Fatalf("consolidate called the model %d times, want 1", analyst.calls)
	}
	if len(candidates.records) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates.records))
	}
	record := candidates.records[0]
	if record.Node.Status != "candidate" || record.Node.DefaultRetrieval {
		t.Fatalf("consolidated candidate = %+v", record.Node)
	}
	if len(record.Evidence) != 2 {
		t.Fatalf("consolidated candidate evidence = %#v", record.Evidence)
	}
	if len(runs.started) != 1 || runs.started[0].Trigger != ports.TriggerSchedule {
		t.Fatalf("consolidate run record = %#v", runs.started)
	}
	if len(runs.finished) != 1 || runs.finished[0].Status != ports.AnalysisRunSucceeded || runs.finished[0].CandidateCount != 1 {
		t.Fatalf("consolidate finished run = %#v", runs.finished)
	}
}

func TestExplicitMemoryIntentIsHandledImmediately(t *testing.T) {
	event := observation.Event{
		ID:          "55555555-5555-4555-8555-555555555555",
		TenantID:    "tenant-a",
		SessionID:   "66666666-6666-4666-8666-666666666666",
		SourceType:  observation.SourceUser,
		MessageType: "message",
		Payload:     json.RawMessage(`{"remember":true,"text":"I always want dark mode"}`),
		OccurredAt:  time.Now().UTC(),
	}
	analyst := &countingAnalyst{result: ports.AnalystResult{Candidates: []ports.CandidateMemory{{
		CandidateID:      "immediate-1",
		EvidenceEventIDs: []string{event.ID},
		ScopeType:        "user-global",
		ScopeID:          "user-1",
		Content:          json.RawMessage(`{"text":"user wants dark mode"}`),
		Confidence:       0.8,
	}}}}
	candidates := &fakeCandidateWriter{}
	runs := &fakeRunStore{}
	dispatcher := dispatcherFor(t, event, analyst, candidates, &fakeNormalizedStore{}, runs)

	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
		ID: "job-immediate", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	// The candidate is produced on the event path, without waiting for a
	// maintenance window, and the run is marked as an explicit trigger.
	if len(candidates.records) != 1 {
		t.Fatalf("immediate candidates = %d, want 1", len(candidates.records))
	}
	if len(runs.started) != 1 || runs.started[0].Trigger != ports.TriggerExplicit || runs.started[0].TaskType != observation.TaskConsolidation {
		t.Fatalf("immediate run = %#v", runs.started)
	}
}

func TestSessionCompactionExtractionProducesActiveWorkingMemory(t *testing.T) {
	event := observation.Event{
		ID:          "77777777-7777-4777-8777-777777777777",
		TenantID:    "tenant-a",
		SessionID:   "88888888-8888-4888-8888-888888888888",
		SourceType:  observation.SourceWorkflow,
		MessageType: "context.compaction",
		Payload:     json.RawMessage(`{"context_summary":true}`),
		OccurredAt:  time.Now().UTC(),
	}
	analyst := &countingAnalyst{result: ports.AnalystResult{Candidates: []ports.CandidateMemory{{
		CandidateID:      "compact-1",
		EvidenceEventIDs: []string{event.ID},
		ScopeType:        "session",
		ScopeID:          event.SessionID,
		Content:          json.RawMessage(`{"text":"the build needs GOPROXY set"}`),
		Confidence:       0.6,
	}}}}
	candidates := &fakeCandidateWriter{}
	runs := &fakeRunStore{}
	dispatcher := dispatcherFor(t, event, analyst, candidates, &fakeNormalizedStore{}, runs)

	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
		ID: "job-compaction", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if analyst.calls != 1 {
		t.Fatalf("compaction boundary called the model %d times, want 1", analyst.calls)
	}
	working := dispatcher.WorkingMemory.(*fakeWorkingMemoryWriter)
	if len(working.records) != 1 {
		t.Fatalf("session working memory = %d, want 1", len(working.records))
	}
	if len(candidates.records) != 0 {
		t.Fatalf("session working memory went to the candidate gate: %d records", len(candidates.records))
	}
	record := working.records[0]
	if record.Node.ScopeType != "session" || record.Node.SessionID != event.SessionID {
		t.Fatalf("session working memory scope = %+v", record.Node)
	}
	// Session working memory is queried during the same session, so the
	// candidate gate must not hold it back; only background memory is gated.
	if record.Node.Status != "active" || !record.Node.DefaultRetrieval {
		t.Fatalf("session working memory must be active and retrievable, got %+v", record.Node)
	}
	if len(runs.started) != 1 || runs.started[0].TaskType != observation.TaskConsolidation || runs.started[0].Trigger != ports.TriggerEvent {
		t.Fatalf("compaction run = %#v", runs.started)
	}
}

func TestCandidateStatusSeparatesWorkingAndBackgroundMemory(t *testing.T) {
	if status, retrievable := candidateStatus("session"); status != "active" || !retrievable {
		t.Fatalf("session scope = (%q, %v), want (active, true)", status, retrievable)
	}
	if status, retrievable := candidateStatus("user-global"); status != "candidate" || retrievable {
		t.Fatalf("user-global scope = (%q, %v), want (candidate, false)", status, retrievable)
	}
}

func TestDeterministicExtractionClosesLoopWithoutLLM(t *testing.T) {
	event := observation.Event{
		ID:          "99999999-9999-4999-8999-999999999999",
		TenantID:    "tenant-a",
		SessionID:   "a9a9a9a9-a9a9-49a9-89a9-a9a9a9a9a9a9",
		SourceType:  observation.SourceWorkflow,
		MessageType: "context.compaction",
		Payload:     json.RawMessage(`{"context_summary":true,"summary":"the build needs GOPROXY"}`),
		OccurredAt:  time.Now().UTC(),
	}
	candidates := &fakeCandidateWriter{}
	runs := &fakeRunStore{}
	// No Memory LLM: the deterministic baseline must still produce memory even
	// though the configured analyst proposes nothing.
	dispatcher := &JobDispatcher{
		Observations:         fakeObservationReader{event: event},
		Normalized:           &fakeNormalizedStore{},
		Analysis:             analysis.New(analysis.Providers{Analyst: analysis.RuleFallback{}}),
		Candidates:           candidates,
		WorkingMemory:        &fakeWorkingMemoryWriter{},
		SessionOwner:         fakeSessionOwner{owner: "user-1"},
		Runs:                 runs,
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		PromptVersion:        "rule-v1",
		NewID:                newUUID,
	}
	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
		ID: "job-deterministic", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(candidates.records) != 0 {
		t.Fatalf("deterministic extraction went to the candidate gate: %d records", len(candidates.records))
	}
	working := dispatcher.WorkingMemory.(*fakeWorkingMemoryWriter)
	if len(working.records) != 1 {
		t.Fatalf("deterministic working memory = %d, want 1", len(working.records))
	}
	record := working.records[0]
	if record.Node.ScopeType != "session" || record.Node.SessionID != event.SessionID {
		t.Fatalf("deterministic scope = %+v", record.Node)
	}
	if record.Node.Status != "active" || !record.Node.DefaultRetrieval {
		t.Fatalf("deterministic session memory must be active, got %+v", record.Node)
	}
	if !strings.Contains(record.Node.ContentText, "GOPROXY") {
		t.Fatalf("deterministic content text = %q", record.Node.ContentText)
	}
}

type fakeCache struct{ values map[string][]byte }

func (f *fakeCache) Get(_ context.Context, tenantID, key string) ([]byte, bool, error) {
	value, ok := f.values[tenantID+"|"+key]
	return value, ok, nil
}

func (f *fakeCache) Put(_ context.Context, tenantID, key string, value []byte, _ time.Duration) error {
	if f.values == nil {
		f.values = map[string][]byte{}
	}
	f.values[tenantID+"|"+key] = value
	return nil
}

func (f *fakeCache) Delete(_ context.Context, tenantID, key string) error {
	delete(f.values, tenantID+"|"+key)
	return nil
}

func TestIdenticalConsolidationReusesCachedResult(t *testing.T) {
	events := []ports.PendingAnalysisEvent{
		{NormalizedID: "n-a", RawEventID: "raw-a", SessionID: "session-1", OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"text":"a"}`)},
	}
	analyst := &countingAnalyst{result: ports.AnalystResult{Candidates: []ports.CandidateMemory{{
		CandidateID:      "cached-1",
		EvidenceEventIDs: []string{"raw-a"},
		ScopeType:        "user-global",
		ScopeID:          "user-1",
		Content:          json.RawMessage(`{"text":"cached"}`),
		Confidence:       0.6,
	}}}}
	candidates := &fakeCandidateWriter{}
	dispatcher := dispatcherFor(t, observation.Event{}, analyst, candidates, &fakeNormalizedStore{}, &fakeRunStore{})
	dispatcher.AnalysisReader = fakeAnalysisReader{events: events}
	dispatcher.Cache = &fakeCache{}

	payload, _ := json.Marshal(map[string]any{"raw_event_ids": []string{"raw-a"}})
	for _, jobID := range []string{"job-cache-1", "job-cache-2"} {
		if err := dispatcher.Handle(context.Background(), ports.OutboxJob{
			ID: jobID, TenantID: "tenant-a", JobType: JobConsolidate, Payload: payload,
		}); err != nil {
			t.Fatalf("Handle(%s) error = %v", jobID, err)
		}
	}
	if analyst.calls != 1 {
		t.Fatalf("identical consolidation called the model %d times, want 1 (cached)", analyst.calls)
	}
}

type fakeAudit struct{ events []ports.AuditEventRecord }

func (f *fakeAudit) RecordAuditEvent(_ context.Context, _ string, event ports.AuditEventRecord) error {
	f.events = append(f.events, event)
	return nil
}

type fakeAnalysisMetrics struct {
	runs   int
	status []string
}

func (f *fakeAnalysisMetrics) RecordAnalysis(_, _, status string, _ time.Duration) {
	f.runs++
	f.status = append(f.status, status)
}

func TestDegradedAnalysisRunIsRecorded(t *testing.T) {
	event := observation.Event{
		ID: "77777777-7777-4777-8777-777777777777", TenantID: "tenant-a", SessionID: "88888888-8888-4888-8888-888888888888",
		SourceType: observation.SourceTool, MessageType: "tool.failure",
		Payload: json.RawMessage(`{"status":"error"}`), OccurredAt: time.Now().UTC(),
	}
	analyst := &countingAnalyst{result: ports.AnalystResult{Degraded: true, DegradedReason: "primary_timeout"}}
	candidates := &fakeCandidateWriter{}
	runs := &fakeRunStore{}
	dispatcher := dispatcherFor(t, event, analyst, candidates, &fakeNormalizedStore{}, runs)
	metrics := &fakeAnalysisMetrics{}
	dispatcher.AnalysisMetrics = metrics

	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{ID: "job-degraded", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(runs.finished) != 1 || runs.finished[0].DegradedReason != "primary_timeout" {
		t.Fatalf("degraded run record = %#v", runs.finished)
	}
	if metrics.runs != 1 {
		t.Fatalf("metrics recorded %d runs, want 1", metrics.runs)
	}
}

type failingAnalyst struct{}

func (failingAnalyst) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	return ports.AnalystResult{}, errors.New("provider timed out")
}

func TestPrimaryFailureDegradesWithoutLosingNormalization(t *testing.T) {
	event := observation.Event{
		ID: "99999999-9999-4999-8999-999999999999", TenantID: "tenant-a", SessionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		SourceType: observation.SourceTool, MessageType: "tool.failure",
		Payload: json.RawMessage(`{"status":"error"}`), OccurredAt: time.Now().UTC(),
	}
	candidates := &fakeCandidateWriter{}
	runs := &fakeRunStore{}
	normalized := &fakeNormalizedStore{}
	dispatcher := &JobDispatcher{
		Observations:         fakeObservationReader{event: event},
		Normalized:           normalized,
		Analysis:             analysis.New(analysis.Providers{Analyst: analysis.ResilientService{Primary: failingAnalyst{}, Fallback: analysis.RuleFallback{}}}),
		Candidates:           candidates,
		SessionOwner:         fakeSessionOwner{owner: "user-1"},
		Runs:                 runs,
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		NewID:                newUUID,
	}
	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{ID: "job-degrade", TenantID: "tenant-a", JobType: JobNormalizeEvent, AggregateID: event.ID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	// The event is still normalized and the run records the degradation.
	if normalized.stores != 1 || normalized.marked != 1 {
		t.Fatalf("normalization lost on provider failure: stores=%d marked=%d", normalized.stores, normalized.marked)
	}
	if len(runs.finished) != 1 || runs.finished[0].DegradedReason != "rules_only" {
		t.Fatalf("run did not record degradation: %#v", runs.finished)
	}
	if len(candidates.records) != 0 {
		t.Fatal("degraded rule fallback produced candidates")
	}
}
