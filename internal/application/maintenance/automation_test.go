package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeEvidence struct {
	records map[string][]ports.MemoryEvidenceRecord
}

func (f fakeEvidence) ListMemoryEvidence(_ context.Context, _, memoryID string) ([]ports.MemoryEvidenceRecord, error) {
	return f.records[memoryID], nil
}

type fakeInfluence struct{ excluded map[string][]string }

func (f fakeInfluence) InfluencedSessions(_ context.Context, _, memoryID string, _ []string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for _, sessionID := range f.excluded[memoryID] {
		result[sessionID] = struct{}{}
	}
	return result, nil
}

func candidateNode(id string) ports.MemoryNodeRecord {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	return ports.MemoryNodeRecord{
		ID: id, IdempotencyKey: "idem-" + id, UserID: "user-1", ScopeType: "user-global", ScopeID: "user-1",
		MemoryType: "preference", Status: "candidate", Visibility: "private", Confidence: 0.2,
		Applicability: json.RawMessage(`{}`), Content: json.RawMessage(`{"text":"prefers concise answers"}`),
		ContentText: "prefers concise answers", Version: 1, Provenance: json.RawMessage(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}
}

func highReliability(id, session string, attribution string) ports.MemoryEvidenceRecord {
	return ports.MemoryEvidenceRecord{
		ID: "ev-" + id, MemoryID: "m1", RawEventID: id, EvidenceRole: "supports",
		Confidence: 0.8, Attribution: attribution, Reliability: "high", SourceSessionID: session,
	}
}

func TestPromotionJobPromotesOnIndependentRecurrence(t *testing.T) {
	governance := &fakeGovernance{}
	job := &PromotionJob{
		PlanName: "promotion",
		Reader:   fakeCandidates{nodes: []ports.MemoryNodeRecord{candidateNode("m1")}},
		Evidence: fakeEvidence{records: map[string][]ports.MemoryEvidenceRecord{
			"m1": {
				highReliability("e1", "session-1", "correlated"),
				highReliability("e2", "session-2", "correlated"),
				highReliability("e3", "session-3", "correlated"),
			},
		}},
		Influence:  fakeInfluence{},
		Governance: governance,
		Runs:       &fakeRuns{},
		NewID:      func() (string, error) { return "id", nil },
		Clock:      func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(governance.commands) != 1 {
		t.Fatalf("applied %d governance actions, want 1", len(governance.commands))
	}
	command := governance.commands[0]
	if command.Action != memory.GovernanceActivate || command.Promotion == nil || command.Promotion.TargetStatus != memory.StatusActive {
		t.Fatalf("command = %#v", command)
	}
	if command.Promotion.Reason != "independent_session_recurrence" {
		t.Fatalf("reason = %q", command.Promotion.Reason)
	}
}

func TestPromotionJobExcludesInfluencedSessions(t *testing.T) {
	governance := &fakeGovernance{}
	job := &PromotionJob{
		PlanName: "promotion",
		Reader:   fakeCandidates{nodes: []ports.MemoryNodeRecord{candidateNode("m1")}},
		Evidence: fakeEvidence{records: map[string][]ports.MemoryEvidenceRecord{
			"m1": {
				highReliability("e1", "session-1", "correlated"),
				highReliability("e2", "session-2", "correlated"),
				highReliability("e3", "session-3", "correlated"),
			},
		}},
		// The memory was injected into session-3, so it is an echo, not a witness.
		Influence:  fakeInfluence{excluded: map[string][]string{"m1": {"session-3"}}},
		Governance: governance,
		Runs:       &fakeRuns{},
		NewID:      func() (string, error) { return "id", nil },
		Clock:      func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(governance.commands) != 0 {
		t.Fatalf("influenced session still promoted: %#v", governance.commands)
	}
}

func TestPromotionJobLeavesUnknownReliabilityAlone(t *testing.T) {
	governance := &fakeGovernance{}
	job := &PromotionJob{
		PlanName: "promotion",
		Reader:   fakeCandidates{nodes: []ports.MemoryNodeRecord{candidateNode("m1")}},
		Evidence: fakeEvidence{records: map[string][]ports.MemoryEvidenceRecord{
			"m1": {{
				ID: "ev-1", MemoryID: "m1", RawEventID: "e1", EvidenceRole: "supports",
				Confidence: 1, Attribution: "inferred", Reliability: "unknown", SourceSessionID: "session-1",
			}},
		}},
		Influence:  fakeInfluence{},
		Governance: governance,
		Runs:       &fakeRuns{},
		NewID:      func() (string, error) { return "id", nil },
		Clock:      func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(governance.commands) != 0 {
		t.Fatalf("unknown reliability promoted: %#v", governance.commands)
	}
}

type fakeTriage struct{ records []ports.CandidateTriageRecord }

func (f *fakeTriage) PersistCandidateTriage(_ context.Context, _ string, records []ports.CandidateTriageRecord) error {
	f.records = append(f.records, records...)
	return nil
}

type fakeEvents struct {
	events map[string]ports.PendingAnalysisEvent
}

func (f fakeEvents) ListPendingAnalysisEvents(context.Context, string, int, int) ([]ports.PendingAnalysisEvent, error) {
	return nil, nil
}

func (f fakeEvents) LoadAnalysisEventsByRawIDs(_ context.Context, _ string, rawIDs []string) ([]ports.PendingAnalysisEvent, error) {
	result := make([]ports.PendingAnalysisEvent, 0, len(rawIDs))
	for _, id := range rawIDs {
		if event, ok := f.events[id]; ok {
			result = append(result, event)
		}
	}
	return result, nil
}

type fakeCandidateWriter struct{ records []ports.CandidateMemoryRecord }

func (f *fakeCandidateWriter) PersistCandidates(_ context.Context, _ string, records []ports.CandidateMemoryRecord) (int, error) {
	f.records = append(f.records, records...)
	return len(records), nil
}

type fakeRelations struct{ relations []ports.MemoryRelationRecord }

func (f *fakeRelations) AddMemoryRelations(_ context.Context, _ string, relations []ports.MemoryRelationRecord) error {
	f.relations = append(f.relations, relations...)
	return nil
}

func TestTriageJobRecordsDuplicatesWithoutAModel(t *testing.T) {
	triage := &fakeTriage{}
	job := &TriageJob{
		PlanName: "triage",
		Reader: fakeCandidates{nodes: []ports.MemoryNodeRecord{
			candidateNode("m1"), candidateNode("m2"),
		}},
		Evidence: fakeEvidence{},
		Triage:   triage,
		Runs:     &fakeRuns{},
		NewID:    func() (string, error) { return "id", nil },
		Clock:    func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(triage.records) != 2 {
		t.Fatalf("triage records = %d, want 2", len(triage.records))
	}
	if triage.records[0].DuplicateGroup == "" || triage.records[0].DuplicateGroup != triage.records[1].DuplicateGroup {
		t.Fatalf("duplicate grouping = %#v", triage.records)
	}
}

type abstractionAnalyst struct{ result ports.AnalystResult }

func (a abstractionAnalyst) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	return a.result, nil
}

func TestTriageJobPersistsAbstractionWithLineage(t *testing.T) {
	writer := &fakeCandidateWriter{}
	relations := &fakeRelations{}
	service := analysis.New(analysis.Providers{Analyst: abstractionAnalyst{result: ports.AnalystResult{
		Candidates: []ports.CandidateMemory{{
			CandidateID: "abstract-1", ScopeType: "user-global", ScopeID: "user-1",
			Content: json.RawMessage(`{"summary":"recurs across sessions"}`), Confidence: 0.7,
			EvidenceEventIDs: []string{"e1", "e2"},
		}},
	}}})
	job := &TriageJob{
		PlanName: "triage",
		Reader: fakeCandidates{nodes: []ports.MemoryNodeRecord{
			candidateNode("m1"), candidateNode("m2"),
		}},
		Evidence: fakeEvidence{records: map[string][]ports.MemoryEvidenceRecord{
			"m1": {{ID: "ev-1", MemoryID: "m1", RawEventID: "e1", EvidenceRole: "supports", Confidence: 0.7, Attribution: "disabled", Reliability: "high", SourceSessionID: "session-1"}},
			"m2": {{ID: "ev-2", MemoryID: "m2", RawEventID: "e2", EvidenceRole: "supports", Confidence: 0.7, Attribution: "disabled", Reliability: "high", SourceSessionID: "session-2"}},
		}},
		Events: fakeEvents{events: map[string]ports.PendingAnalysisEvent{
			"e1": {RawEventID: "e1", SessionID: "session-1", OccurredAt: time.Unix(0, 0), Payload: json.RawMessage(`{}`), Attribution: "direct"},
			"e2": {RawEventID: "e2", SessionID: "session-2", OccurredAt: time.Unix(0, 0), Payload: json.RawMessage(`{}`), Attribution: "direct"},
		}},
		Candidates: writer, Relations: relations, Triage: &fakeTriage{},
		Analysis: service, Runs: &fakeRuns{}, PromptVersion: "propose_transfer-llm-v1", SchemaVersion: "schema-v1",
		NewID: func() (string, error) { return "generated", nil },
		Clock: func() time.Time { return time.Unix(0, 0) },
	}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(writer.records) != 1 {
		t.Fatalf("abstraction records = %d, want 1", len(writer.records))
	}
	record := writer.records[0]
	if record.Node.Status != "candidate" || record.Node.ScopeType != "user-global" {
		t.Fatalf("abstraction node = %#v", record.Node)
	}
	if len(relations.relations) != 2 || relations.relations[0].RelationType != "generalizes" {
		t.Fatalf("lineage relations = %#v", relations.relations)
	}
}

// TestTriageAbstractionThenPromotionIsAComposedFlow checks the two halves work
// together: an abstracted, cross-session candidate can be promoted by the
// evidence policy without any model self-assessment.
func TestTriageAbstractionThenPromotionIsAComposedFlow(t *testing.T) {
	writer := &fakeCandidateWriter{}
	service := analysis.New(analysis.Providers{Analyst: abstractionAnalyst{result: ports.AnalystResult{
		Candidates: []ports.CandidateMemory{{
			CandidateID: "abstract-1", ScopeType: "user-global", ScopeID: "user-1",
			Content: json.RawMessage(`{"summary":"recurs across sessions"}`), Confidence: 0.7,
			EvidenceEventIDs: []string{"e1", "e2", "e3"},
		}},
	}}})
	triage := &TriageJob{
		PlanName: "triage",
		Reader: fakeCandidates{nodes: []ports.MemoryNodeRecord{
			candidateNode("m1"), candidateNode("m2"), candidateNode("m3"),
		}},
		Evidence: fakeEvidence{records: map[string][]ports.MemoryEvidenceRecord{
			"m1": {{ID: "ev-1", MemoryID: "m1", RawEventID: "e1", EvidenceRole: "supports", Confidence: 0.7, Attribution: "direct", Reliability: "high", SourceSessionID: "session-1"}},
			"m2": {{ID: "ev-2", MemoryID: "m2", RawEventID: "e2", EvidenceRole: "supports", Confidence: 0.7, Attribution: "direct", Reliability: "high", SourceSessionID: "session-2"}},
			"m3": {{ID: "ev-3", MemoryID: "m3", RawEventID: "e3", EvidenceRole: "supports", Confidence: 0.7, Attribution: "direct", Reliability: "high", SourceSessionID: "session-3"}},
		}},
		Events: fakeEvents{events: map[string]ports.PendingAnalysisEvent{
			"e1": {RawEventID: "e1", SessionID: "session-1", OccurredAt: time.Unix(0, 0), Payload: json.RawMessage(`{}`), Attribution: "direct"},
			"e2": {RawEventID: "e2", SessionID: "session-2", OccurredAt: time.Unix(0, 0), Payload: json.RawMessage(`{}`), Attribution: "direct"},
			"e3": {RawEventID: "e3", SessionID: "session-3", OccurredAt: time.Unix(0, 0), Payload: json.RawMessage(`{}`), Attribution: "direct"},
		}},
		Candidates: writer, Relations: &fakeRelations{}, Triage: &fakeTriage{},
		Analysis: service, Runs: &fakeRuns{}, PromptVersion: "propose_transfer-llm-v1", SchemaVersion: "schema-v1",
		NewID: func() (string, error) { return "generated", nil },
		Clock: func() time.Time { return time.Unix(0, 0) },
	}
	if err := triage.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("triage Run() error = %v", err)
	}
	if len(writer.records) != 1 {
		t.Fatalf("abstraction records = %d, want 1", len(writer.records))
	}
	abstracted := writer.records[0].Node
	abstracted.Applicability = json.RawMessage(`{}`)

	governance := &fakeGovernance{}
	promotion := &PromotionJob{
		PlanName: "promotion",
		Reader:   fakeCandidates{nodes: []ports.MemoryNodeRecord{abstracted}},
		Evidence: fakeEvidence{records: map[string][]ports.MemoryEvidenceRecord{
			"generated": {
				{ID: "ev-1", MemoryID: "generated", RawEventID: "e1", EvidenceRole: "supports", Confidence: 0.8, Attribution: "direct", Reliability: "high", SourceSessionID: "session-1"},
				{ID: "ev-2", MemoryID: "generated", RawEventID: "e2", EvidenceRole: "supports", Confidence: 0.8, Attribution: "direct", Reliability: "high", SourceSessionID: "session-2"},
				{ID: "ev-3", MemoryID: "generated", RawEventID: "e3", EvidenceRole: "supports", Confidence: 0.8, Attribution: "direct", Reliability: "high", SourceSessionID: "session-3"},
			},
		}},
		Influence: fakeInfluence{}, Governance: governance, Runs: &fakeRuns{},
		NewID: func() (string, error) { return "audit", nil },
		Clock: func() time.Time { return time.Unix(0, 0) },
	}
	if err := promotion.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("promotion Run() error = %v", err)
	}
	if len(governance.commands) != 1 || governance.commands[0].Promotion == nil || governance.commands[0].Promotion.TargetStatus != memory.StatusActive {
		t.Fatalf("composed flow did not promote the abstraction: %#v", governance.commands)
	}
}
