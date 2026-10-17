package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// TriageJob scans recent candidates for duplicates, conflicts, and cross-session
// common problems. When the Memory LLM abstracts a common problem, it records a
// new user-global candidate and links it to its sources through `generalizes`.
//
// It is strictly advisory: it never changes a candidate's status, confidence, or
// retrieval flag. Promotion stays with governance.
type TriageJob struct {
	PlanName      string
	Reader        ports.RecentCandidateReader
	Evidence      ports.MemoryEvidenceReader
	Events        ports.NormalizedEventReader
	Candidates    ports.CandidateMemoryWriter
	Relations     ports.MemoryRelationWriter
	Triage        ports.CandidateTriageWriter
	Analysis      *analysis.Service
	Runs          ports.AnalysisRunStore
	PromptVersion string
	SchemaVersion string
	BatchSize     int
	NewID         func() (string, error)
	Clock         func() time.Time
}

func (j *TriageJob) Name() string { return j.PlanName }

func (j *TriageJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Triage == nil {
		return errors.New("triage job is not configured")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	nodes, err := j.Reader.ListRecentCandidates(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	if len(nodes) < 2 {
		return nil
	}
	now := j.now()
	// Triage is per owner: a cross-session problem is only meaningful within one
	// user's memory tree, and an abstracted candidate must stay scoped to them.
	byOwner := map[string][]ports.MemoryNodeRecord{}
	for _, node := range nodes {
		if node.UserID == "" {
			continue
		}
		byOwner[node.UserID] = append(byOwner[node.UserID], node)
	}
	created := 0
	for owner, group := range byOwner {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := j.triageOwner(ctx, tenantID, owner, group, now)
		if err != nil {
			return err
		}
		created += n
	}
	return recordRun(ctx, j.Runs, j.NewID, runSpec{
		tenantID: tenantID, plan: j.PlanName, task: "candidate_triage", now: now, candidateCount: created,
	})
}

func (j *TriageJob) triageOwner(ctx context.Context, tenantID, owner string, nodes []ports.MemoryNodeRecord, now time.Time) (int, error) {
	correlation := correlationID(j.PlanName+":"+owner, tenantID, now)
	// Deterministic duplicate grouping always runs, even without a model.
	groupByNode := make(map[string]string, len(nodes))
	for _, node := range nodes {
		groupByNode[node.ID] = duplicateKey(node)
	}
	eventByID := map[string]ports.PendingAnalysisEvent{}
	nodeByEvent := map[string]string{}
	candidates := make([]ports.CandidateMemory, 0, len(nodes))
	for _, node := range nodes {
		records, err := j.Evidence.ListMemoryEvidence(ctx, tenantID, node.ID)
		if err != nil {
			return 0, err
		}
		evidenceIDs := make([]string, 0, len(records))
		for _, record := range records {
			evidenceIDs = append(evidenceIDs, record.RawEventID)
		}
		candidates = append(candidates, ports.CandidateMemory{
			CandidateID: node.ID, ScopeType: node.ScopeType, ScopeID: node.ScopeID,
			Content: node.Content, Confidence: node.Confidence, EvidenceEventIDs: evidenceIDs,
		})
	}
	if j.Analysis == nil || j.Events == nil || len(candidates) < 2 {
		return 0, j.recordTriage(ctx, tenantID, correlation, nodes, groupByNode, nil, now)
	}
	rawIDs := make([]string, 0)
	for _, candidate := range candidates {
		for _, id := range candidate.EvidenceEventIDs {
			if _, seen := eventByID[id]; !seen {
				eventByID[id] = ports.PendingAnalysisEvent{}
				rawIDs = append(rawIDs, id)
			}
		}
	}
	if len(rawIDs) == 0 {
		return 0, j.recordTriage(ctx, tenantID, correlation, nodes, groupByNode, nil, now)
	}
	loaded, err := j.Events.LoadAnalysisEventsByRawIDs(ctx, tenantID, rawIDs)
	if err != nil {
		return 0, err
	}
	events := make([]ports.AnalysisEvent, 0, len(loaded))
	for _, event := range loaded {
		eventByID[event.RawEventID] = event
		events = append(events, ports.AnalysisEvent{
			EventID: event.RawEventID, SessionID: event.SessionID, OccurredAt: event.OccurredAt, Payload: event.Payload,
		})
	}
	for _, node := range nodes {
		records, err := j.Evidence.ListMemoryEvidence(ctx, tenantID, node.ID)
		if err != nil {
			return 0, err
		}
		for _, record := range records {
			if _, ok := nodeByEvent[record.RawEventID]; !ok {
				nodeByEvent[record.RawEventID] = node.ID
			}
		}
	}
	started := j.now()
	result, err := j.Analysis.Analyze(ctx, ports.AnalysisBatch{
		TenantID: tenantID, RunID: correlation, TaskType: observation.TaskProposeTransfer,
		Events: events, Candidates: candidates,
		PromptVersion: j.PromptVersion, SchemaVersion: j.SchemaVersion,
	})
	if err != nil {
		return 0, err
	}
	if err := j.recordTriage(ctx, tenantID, correlation, nodes, groupByNode, result.Conflicts, now); err != nil {
		return 0, err
	}
	created, err := j.persistAbstractions(ctx, tenantID, owner, correlation, result.Candidates, eventByID, nodeByEvent, now)
	if err != nil {
		return 0, err
	}
	if j.Runs != nil {
		_, _ = j.Runs.FinishAnalysisRun(context.WithoutCancel(ctx), tenantID, correlation, ports.AnalysisRunUpdate{
			Status: ports.AnalysisRunSucceeded, CandidateCount: created,
			LatencyMS: j.now().Sub(started).Milliseconds(),
		})
	}
	return created, nil
}

func (j *TriageJob) recordTriage(ctx context.Context, tenantID, runID string, nodes []ports.MemoryNodeRecord, groupByNode map[string]string, conflicts []ports.ConflictAssessment, now time.Time) error {
	conflictByNode := map[string][]string{}
	reasonByNode := map[string]string{}
	for _, conflict := range conflicts {
		if !conflict.Conflicted {
			continue
		}
		for _, id := range conflict.CandidateIDs {
			for _, other := range conflict.CandidateIDs {
				if other != id {
					conflictByNode[id] = append(conflictByNode[id], other)
				}
			}
			if conflict.ReasonCode != "" {
				reasonByNode[id] = conflict.ReasonCode
			}
		}
	}
	records := make([]ports.CandidateTriageRecord, 0, len(nodes))
	for _, node := range nodes {
		id, err := j.newID()
		if err != nil {
			return err
		}
		records = append(records, ports.CandidateTriageRecord{
			ID: id, MemoryID: node.ID, RunID: runID,
			DuplicateGroup: groupByNode[node.ID], ConflictIDs: conflictByNode[node.ID],
			Priority: 0, Rationale: reasonByNode[node.ID], CreatedAt: now,
		})
	}
	return j.Triage.PersistCandidateTriage(ctx, tenantID, records)
}

// persistAbstractions writes each abstracted candidate and links it to the
// source candidates its evidence overlaps.
func (j *TriageJob) persistAbstractions(ctx context.Context, tenantID, owner, runID string, abstractions []ports.CandidateMemory, eventByID map[string]ports.PendingAnalysisEvent, nodeByEvent map[string]string, now time.Time) (int, error) {
	if len(abstractions) == 0 || j.Candidates == nil {
		return 0, nil
	}
	created := 0
	for _, candidate := range abstractions {
		if candidate.ScopeType != "" && candidate.ScopeType != "user-global" {
			continue
		}
		id, err := j.newID()
		if err != nil {
			return created, err
		}
		provenance, err := json.Marshal(map[string]any{
			"candidate_id":   candidate.CandidateID,
			"task_type":      observation.TaskProposeTransfer,
			"prompt_version": j.PromptVersion,
			"evidence":       candidate.EvidenceEventIDs,
		})
		if err != nil {
			return created, err
		}
		evidence := make([]ports.MemoryEvidenceRecord, 0, len(candidate.EvidenceEventIDs))
		sourceIDs := make([]string, 0, len(candidate.EvidenceEventIDs))
		for _, evidenceID := range candidate.EvidenceEventIDs {
			attribution := eventByID[evidenceID].Attribution
			if attribution == "" {
				attribution = "inferred"
			}
			evidence = append(evidence, ports.MemoryEvidenceRecord{
				MemoryID: id, RawEventID: evidenceID, EvidenceRole: "supports",
				Confidence: candidate.Confidence, Attribution: attribution,
			})
			if sourceID, ok := nodeByEvent[evidenceID]; ok {
				sourceIDs = append(sourceIDs, sourceID)
			}
		}
		record := ports.CandidateMemoryRecord{Node: ports.MemoryNodeRecord{
			ID:               id,
			IdempotencyKey:   "triage:" + runID + ":" + candidate.CandidateID,
			UserID:           owner,
			ScopeType:        "user-global",
			ScopeID:          owner,
			MemoryType:       "insight",
			Status:           "candidate",
			Visibility:       "private",
			Confidence:       candidate.Confidence,
			Content:          candidate.Content,
			ContentText:      triageContentText(candidate.Content),
			DefaultRetrieval: false,
			Provenance:       provenance,
		}, Evidence: evidence}
		if _, err := j.Candidates.PersistCandidates(ctx, tenantID, []ports.CandidateMemoryRecord{record}); err != nil {
			return created, err
		}
		created++
		if j.Relations != nil && len(sourceIDs) > 0 {
			relations := make([]ports.MemoryRelationRecord, 0, len(sourceIDs))
			seen := map[string]struct{}{}
			for _, sourceID := range sourceIDs {
				if sourceID == id {
					continue
				}
				if _, ok := seen[sourceID]; ok {
					continue
				}
				seen[sourceID] = struct{}{}
				relationID, err := j.newID()
				if err != nil {
					return created, err
				}
				relations = append(relations, ports.MemoryRelationRecord{
					ID: relationID, SourceMemoryID: id, TargetMemoryID: sourceID,
					RelationType: "generalizes", Confidence: candidate.Confidence, CreatedAt: now,
				})
			}
			if len(relations) > 0 {
				if err := j.Relations.AddMemoryRelations(ctx, tenantID, relations); err != nil {
					return created, err
				}
			}
		}
	}
	return created, nil
}

func triageContentText(content json.RawMessage) string {
	return memory.RenderContentText(content)
}

func (j *TriageJob) now() time.Time         { return clock(j.Clock) }
func (j *TriageJob) newID() (string, error) { return newIDOr(j.NewID) }
