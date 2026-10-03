package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// ErrUnsupportedJobType marks an outbox job the worker does not understand. It
// is returned (not swallowed) so the durable retry/dead-letter path records it.
var ErrUnsupportedJobType = errors.New("unsupported outbox job type")

// AnalysisMetrics records analysis run outcomes for observability.
type AnalysisMetrics interface {
	RecordAnalysis(tenantID, task, status string, duration time.Duration)
}

// Job type identifiers persisted in outbox_jobs.
const (
	JobNormalizeEvent = "normalize_event"
	JobConsolidate    = ConsolidateJobType
)

// JobDispatcher routes durable outbox jobs to handlers. Normalization turns a
// raw observation into a persisted normalized event. Only events that pass the
// local rule pre-screen (failure, retry, user correction, or explicit memory
// intent) call the Memory LLM; ordinary events are normalized alone and may
// never produce candidates.
type JobDispatcher struct {
	Observations   ports.ObservationReader
	Normalized     ports.NormalizedEventStore
	Analysis       *analysis.Service
	AnalysisReader ports.NormalizedEventReader
	Candidates     ports.CandidateMemoryWriter
	// WorkingMemory persists session-scoped working memory, which is active on
	// arrival. The user-global background tree stays on Candidates and is
	// promoted only by governance.
	WorkingMemory ports.WorkingMemoryWriter
	SessionOwner  ports.SessionScopeRepository
	Runs          ports.AnalysisRunStore
	// Cache reuses an identical consolidation result instead of calling the
	// model again. It is keyed by tenant, prompt version, schema and inputs.
	Cache    ports.TenantCache
	CacheTTL time.Duration
	// AnalysisMetrics records analysis run outcomes per tenant and task.
	AnalysisMetrics AnalysisMetrics
	// Audit records candidate provenance durably. It never stores secrets.
	Audit                ports.AuditWriter
	SchemaVersion        string
	NormalizationVersion string
	PromptVersion        string
	Provider             string
	Model                string
	NewID                func() (string, error)
	// AnalysisTask is the rule pre-screen. It defaults to observation.AnalysisTask.
	AnalysisTask func(observation.Event) (string, bool)
}

// Handle implements workers.JobHandler.
func (d *JobDispatcher) Handle(ctx context.Context, job ports.OutboxJob) error {
	switch job.JobType {
	case JobNormalizeEvent:
		return d.normalizeEvent(ctx, job)
	case JobConsolidate:
		return d.consolidate(ctx, job)
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedJobType, job.JobType)
	}
}

func (d *JobDispatcher) normalizeEvent(ctx context.Context, job ports.OutboxJob) error {
	if d == nil || d.Observations == nil || d.Normalized == nil || d.Analysis == nil {
		return errors.New("normalization job handler is not configured")
	}
	rawEventID := job.AggregateID
	if rawEventID == "" {
		var payload struct {
			RawEventID string `json:"raw_event_id"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("decode normalize job payload: %w", err)
		}
		rawEventID = payload.RawEventID
	}
	if rawEventID == "" {
		return errors.New("normalize job has no raw event id")
	}
	event, err := d.Observations.GetObservation(ctx, job.TenantID, rawEventID)
	if err != nil {
		return err
	}
	taskFor := d.AnalysisTask
	if taskFor == nil {
		taskFor = observation.AnalysisTask
	}
	taskType, gated := taskFor(event)
	var analysisResult ports.AnalystResult
	if gated && d.Analysis != nil {
		promptVersion := d.PromptVersion
		if promptVersion == "" {
			promptVersion = "rule-v1"
		}
		started := time.Now()
		runID, err := d.startRun(ctx, job, event, taskType, promptVersion)
		if err != nil {
			return err
		}
		analysisResult, err = d.Analysis.Analyze(ctx, ports.AnalysisBatch{
			TenantID:      job.TenantID,
			RunID:         job.ID,
			TaskType:      taskType,
			PromptVersion: promptVersion,
			SchemaVersion: d.SchemaVersion,
			Events:        []ports.AnalysisEvent{{EventID: event.ID, SessionID: event.SessionID, OccurredAt: event.OccurredAt, MessageType: event.MessageType, Payload: event.Payload}},
		})
		if err != nil {
			d.finishRun(ctx, job.TenantID, runID, taskType, time.Since(started), ports.AnalysisRunUpdate{
				Status:    ports.AnalysisRunFailed,
				LastError: err.Error(),
				LatencyMS: time.Since(started).Milliseconds(),
			})
			return err
		}
		d.captureDeterministicCandidate(event, &analysisResult)
		d.finishRun(ctx, job.TenantID, runID, taskType, time.Since(started), runUpdateFromResult(analysisResult, time.Since(started)))
	}
	pipeline, err := analysis.BuildPipelineResult(job.TenantID, d.SchemaVersion, d.NormalizationVersion, []analysis.RawEvent{{
		ID:            event.ID,
		TenantID:      job.TenantID,
		SessionID:     event.SessionID,
		OccurredAt:    event.OccurredAt,
		SourceType:    string(event.SourceType),
		MessageType:   event.MessageType,
		Payload:       event.Payload,
		Sequence:      event.Sequence,
		ParentEventID: event.ParentEventID,
	}}, analysisResult, time.Now().UTC())
	if err != nil {
		return err
	}
	newID := d.NewID
	if newID == nil {
		newID = newUUID
	}
	records := make([]ports.NormalizedEventRecord, 0, len(pipeline.Normalized))
	for _, item := range pipeline.Normalized {
		id, err := newID()
		if err != nil {
			return err
		}
		records = append(records, ports.NormalizedEventRecord{
			ID:                   id,
			RawEventID:           item.RawEventID,
			SchemaVersion:        item.SchemaVersion,
			NormalizationVersion: item.NormalizationVersion,
			Sequence:             item.Sequence,
			ParentEventID:        item.ParentEventID,
			Payload:              item.Payload,
		})
	}
	if err := d.Normalized.StoreNormalizedEvents(ctx, job.TenantID, records); err != nil {
		return err
	}
	if gated {
		if err := d.persistCandidates(ctx, job, event, pipeline.Candidates); err != nil {
			return err
		}
	}
	return d.Normalized.MarkRawEventProcessed(ctx, job.TenantID, event.ID)
}

// startRun opens an audit record for a gated analysis run. It is a no-op when
// no run store is configured. It returns the correlation id (the outbox job id)
// so the terminal update can find the same record.
func (d *JobDispatcher) startRun(ctx context.Context, job ports.OutboxJob, event observation.Event, taskType, promptVersion string) (string, error) {
	trigger := ports.TriggerEvent
	if taskType == observation.TaskConsolidation && observation.HasMemoryIntent(event) {
		trigger = ports.TriggerExplicit
	}
	return d.startRunRecord(ctx, job, taskType, trigger, promptVersion, event.SessionID, []string{event.ID})
}

func (d *JobDispatcher) startRunRecord(ctx context.Context, job ports.OutboxJob, taskType, trigger, promptVersion, sessionID string, inputEventIDs []string) (string, error) {
	if d.Runs == nil {
		return job.ID, nil
	}
	newID := d.NewID
	if newID == nil {
		newID = newUUID
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	_, err = d.Runs.StartAnalysisRun(ctx, job.TenantID, ports.AnalysisRunRecord{
		ID:            id,
		RunID:         job.ID,
		TaskType:      taskType,
		Trigger:       trigger,
		Provider:      d.Provider,
		Model:         d.Model,
		PromptVersion: promptVersion,
		SchemaVersion: d.SchemaVersion,
		SessionID:     sessionID,
		InputEventIDs: inputEventIDs,
		Status:        ports.AnalysisRunRunning,
	})
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

// consolidate runs periodic maintenance for one batch of normalized events: it
// asks the Memory LLM for durable candidates and persists them (as candidates)
// with their evidence. Governance owns any promotion.
func (d *JobDispatcher) consolidate(ctx context.Context, job ports.OutboxJob) error {
	if d.AnalysisReader == nil || d.Analysis == nil {
		return errors.New("consolidation handler is not configured")
	}
	var payload struct {
		RawEventIDs []string `json:"raw_event_ids"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode consolidate job payload: %w", err)
	}
	if len(payload.RawEventIDs) == 0 {
		return nil
	}
	events, err := d.AnalysisReader.LoadAnalysisEventsByRawIDs(ctx, job.TenantID, payload.RawEventIDs)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	promptVersion := d.PromptVersion
	if promptVersion == "" {
		promptVersion = "rule-v1"
	}
	batchEvents := make([]ports.AnalysisEvent, 0, len(events))
	evidenceIDs := make([]string, 0, len(events))
	sessionID := ""
	for _, event := range events {
		batchEvents = append(batchEvents, ports.AnalysisEvent{EventID: event.RawEventID, SessionID: event.SessionID, OccurredAt: event.OccurredAt, Payload: event.Payload})
		evidenceIDs = append(evidenceIDs, event.RawEventID)
		if sessionID == "" {
			sessionID = event.SessionID
		}
	}
	inputHash := analysisInputHash(promptVersion, d.SchemaVersion, evidenceIDs)
	if d.Cache != nil {
		if encoded, ok, err := d.Cache.Get(ctx, job.TenantID, "consolidate:"+inputHash); err == nil && ok {
			var cached ports.AnalystResult
			if json.Unmarshal(encoded, &cached) == nil {
				// Reuse the prior result: no second model call for identical input.
				return d.persistCandidatesForEvents(ctx, job, events, cached.Candidates, promptVersion, inputHash)
			}
		}
	}
	started := time.Now()
	runID, err := d.startRunRecord(ctx, job, observation.TaskConsolidation, ports.TriggerSchedule, promptVersion, sessionID, evidenceIDs)
	if err != nil {
		return err
	}
	result, err := d.Analysis.Analyze(ctx, ports.AnalysisBatch{
		TenantID:      job.TenantID,
		RunID:         job.ID,
		TaskType:      observation.TaskConsolidation,
		PromptVersion: promptVersion,
		SchemaVersion: d.SchemaVersion,
		Events:        batchEvents,
	})
	if err != nil {
		d.finishRun(ctx, job.TenantID, runID, observation.TaskConsolidation, time.Since(started), ports.AnalysisRunUpdate{Status: ports.AnalysisRunFailed, LastError: err.Error(), LatencyMS: time.Since(started).Milliseconds()})
		return err
	}
	d.finishRun(ctx, job.TenantID, runID, observation.TaskConsolidation, time.Since(started), runUpdateFromResult(result, time.Since(started)))
	if d.Cache != nil {
		if encoded, err := json.Marshal(result); err == nil {
			ttl := d.CacheTTL
			if ttl <= 0 {
				ttl = 24 * time.Hour
			}
			_ = d.Cache.Put(ctx, job.TenantID, "consolidate:"+inputHash, encoded, ttl)
		}
	}
	if len(result.Candidates) == 0 {
		return nil
	}
	return d.persistCandidatesForEvents(ctx, job, events, result.Candidates, promptVersion, inputHash)
}

// analysisInputHash is stable for the same prompt, schema and evidence set, so
// both the result cache and candidate idempotency keys dedupe identical work.
func analysisInputHash(promptVersion, schemaVersion string, evidenceIDs []string) string {
	sorted := append([]string(nil), evidenceIDs...)
	sort.Strings(sorted)
	h := sha256.New()
	_, _ = h.Write([]byte(promptVersion))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(schemaVersion))
	for _, id := range sorted {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// persistCandidatesForEvents persists periodic consolidation candidates. Owner
// is resolved from the evidence session so candidates stay correctly scoped.
func (d *JobDispatcher) persistCandidatesForEvents(ctx context.Context, job ports.OutboxJob, events []ports.PendingAnalysisEvent, candidates []ports.CandidateMemory, promptVersion, inputHash string) error {
	if d.Candidates == nil && d.WorkingMemory == nil {
		return nil
	}
	sessionByRawID := make(map[string]string, len(events))
	for _, event := range events {
		sessionByRawID[event.RawEventID] = event.SessionID
	}
	newID := d.NewID
	if newID == nil {
		newID = newUUID
	}
	records := make([]ports.CandidateMemoryRecord, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ScopeType != "session" && candidate.ScopeType != "user-global" {
			continue
		}
		sessionID := ""
		for _, evidenceID := range candidate.EvidenceEventIDs {
			if value, ok := sessionByRawID[evidenceID]; ok && value != "" {
				sessionID = value
				break
			}
		}
		if sessionID == "" || d.SessionOwner == nil {
			continue
		}
		owner, err := d.SessionOwner.GetSessionOwner(ctx, job.TenantID, sessionID)
		if err != nil || owner == "" {
			continue
		}
		scopeID := candidate.ScopeID
		nodeSession := ""
		if candidate.ScopeType == "session" {
			nodeSession = sessionID
			scopeID = sessionID
		} else {
			scopeID = owner
		}
		status, defaultRetrieval := candidateStatus(candidate.ScopeType)
		id, err := newID()
		if err != nil {
			return err
		}
		provenance, err := json.Marshal(map[string]any{
			"candidate_id":   candidate.CandidateID,
			"task_type":      observation.TaskConsolidation,
			"provider":       d.Provider,
			"model":          d.Model,
			"prompt_version": promptVersion,
			"evidence":       candidate.EvidenceEventIDs,
		})
		if err != nil {
			return err
		}
		evidence := make([]ports.MemoryEvidenceRecord, 0, len(candidate.EvidenceEventIDs))
		for _, evidenceID := range candidate.EvidenceEventIDs {
			evidence = append(evidence, ports.MemoryEvidenceRecord{MemoryID: id, RawEventID: evidenceID, EvidenceRole: "supports", Confidence: candidate.Confidence, Attribution: "inferred"})
		}
		records = append(records, ports.CandidateMemoryRecord{Node: ports.MemoryNodeRecord{
			ID:               id,
			IdempotencyKey:   "candidate:" + inputHash + ":" + candidate.CandidateID,
			UserID:           owner,
			SessionID:        nodeSession,
			ScopeType:        candidate.ScopeType,
			ScopeID:          scopeID,
			MemoryType:       "insight",
			Status:           status,
			Visibility:       "private",
			Confidence:       candidate.Confidence,
			Content:          candidate.Content,
			ContentText:      candidateContentText(candidate.Content),
			DefaultRetrieval: defaultRetrieval,
			Provenance:       provenance,
		}, Evidence: evidence})
	}
	if len(records) == 0 {
		return nil
	}
	return d.persistMemoryRecords(ctx, job, records)
}

// persistMemoryRecords routes each gathered record to the writer that owns its
// scope. Session working memory is active on arrival and queryable during the
// same session; the user-global background tree is persisted as candidates and
// promoted only by governance. Audit and run-output linking cover both, so the
// provenance of every produced memory stays in one trail.
func (d *JobDispatcher) persistMemoryRecords(ctx context.Context, job ports.OutboxJob, records []ports.CandidateMemoryRecord) error {
	working := make([]ports.WorkingMemoryRecord, 0, len(records))
	candidates := make([]ports.CandidateMemoryRecord, 0, len(records))
	for _, record := range records {
		if record.Node.ScopeType == "session" {
			working = append(working, ports.WorkingMemoryRecord{Node: record.Node, Evidence: record.Evidence})
			continue
		}
		candidates = append(candidates, record)
	}
	if len(working) > 0 {
		if d.WorkingMemory == nil {
			return errors.New("working memory writer is not configured")
		}
		if _, err := d.WorkingMemory.PersistWorkingMemory(ctx, job.TenantID, working); err != nil {
			return err
		}
	}
	if len(candidates) > 0 {
		if d.Candidates == nil {
			return errors.New("candidate writer is not configured")
		}
		if _, err := d.Candidates.PersistCandidates(ctx, job.TenantID, candidates); err != nil {
			return err
		}
	}
	d.recordCandidateAudit(ctx, job, records)
	if d.Runs != nil {
		ids := make([]string, 0, len(records))
		for _, record := range records {
			ids = append(ids, record.Node.ID)
		}
		_, _ = d.Runs.LinkRunOutputs(context.WithoutCancel(ctx), job.TenantID, job.ID, ids)
	}
	return nil
}

// recordCandidateAudit records durable provenance for each produced memory so
// its origin (model, task, prompt version, evidence) is traceable.
func (d *JobDispatcher) recordCandidateAudit(ctx context.Context, job ports.OutboxJob, records []ports.CandidateMemoryRecord) {
	if d.Audit == nil {
		return
	}
	for _, record := range records {
		_ = d.Audit.RecordAuditEvent(context.WithoutCancel(ctx), job.TenantID, ports.AuditEventRecord{
			ActorType:    "system",
			ActorID:      d.Provider,
			Action:       "candidate.created",
			ResourceType: "memory_candidate",
			ResourceID:   record.Node.ID,
			RequestID:    job.ID,
			Changes:      record.Node.Provenance,
		})
	}
}

func (d *JobDispatcher) finishRun(ctx context.Context, tenantID, runID, task string, elapsed time.Duration, update ports.AnalysisRunUpdate) {
	status := update.Status
	if d.AnalysisMetrics != nil {
		d.AnalysisMetrics.RecordAnalysis(tenantID, task, status, elapsed)
	}
	if d.Runs == nil || runID == "" {
		return
	}
	_, _ = d.Runs.FinishAnalysisRun(context.WithoutCancel(ctx), tenantID, runID, update)
}

func runUpdateFromResult(result ports.AnalystResult, elapsed time.Duration) ports.AnalysisRunUpdate {
	update := ports.AnalysisRunUpdate{
		Status:         ports.AnalysisRunSucceeded,
		LatencyMS:      elapsed.Milliseconds(),
		CandidateCount: len(result.Candidates),
		ConflictCount:  len(result.Conflicts),
	}
	if result.Usage != nil {
		update.TokensPrompt = result.Usage.PromptTokens
		update.TokensCompletion = result.Usage.CompletionTokens
		if result.Usage.LatencyMS > 0 {
			update.LatencyMS = result.Usage.LatencyMS
		}
	}
	if result.Degraded {
		update.DegradedReason = result.DegradedReason
	}
	return update
}

// persistCandidates stores model-proposed memories as candidates. A candidate
// is never promoted here; that stays with governance.
func (d *JobDispatcher) persistCandidates(ctx context.Context, job ports.OutboxJob, event observation.Event, candidates []ports.CandidateMemory) error {
	if len(candidates) == 0 || (d.Candidates == nil && d.WorkingMemory == nil) {
		return nil
	}
	userID := ""
	if event.SessionID != "" && d.SessionOwner != nil {
		owner, err := d.SessionOwner.GetSessionOwner(ctx, job.TenantID, event.SessionID)
		if err != nil {
			return fmt.Errorf("resolve candidate owner: %w", err)
		}
		userID = owner
	}
	if userID == "" {
		// Without a resolvable owner the candidate cannot be scoped safely.
		return nil
	}
	newID := d.NewID
	if newID == nil {
		newID = newUUID
	}
	promptVersion := d.PromptVersion
	if promptVersion == "" {
		promptVersion = "rule-v1"
	}
	records := make([]ports.CandidateMemoryRecord, 0, len(candidates))
	for _, candidate := range candidates {
		id, err := newID()
		if err != nil {
			return err
		}
		sessionID := ""
		scopeID := candidate.ScopeID
		switch candidate.ScopeType {
		case "session":
			sessionID = candidate.ScopeID
		case "user-global":
			scopeID = userID
		default:
			continue
		}
		status, defaultRetrieval := candidateStatus(candidate.ScopeType)
		provenance, err := json.Marshal(map[string]any{
			"candidate_id":   candidate.CandidateID,
			"task_type":      event.MessageType,
			"provider":       d.Provider,
			"model":          d.Model,
			"prompt_version": promptVersion,
			"evidence":       candidate.EvidenceEventIDs,
		})
		if err != nil {
			return err
		}
		node := ports.MemoryNodeRecord{
			ID:               id,
			IdempotencyKey:   "candidate:" + job.ID + ":" + candidate.CandidateID,
			UserID:           userID,
			SessionID:        sessionID,
			ScopeType:        candidate.ScopeType,
			ScopeID:          scopeID,
			MemoryType:       "insight",
			Status:           status,
			Visibility:       "private",
			Confidence:       candidate.Confidence,
			Content:          candidate.Content,
			ContentText:      candidateContentText(candidate.Content),
			DefaultRetrieval: defaultRetrieval,
			Provenance:       provenance,
		}
		evidence := make([]ports.MemoryEvidenceRecord, 0, len(candidate.EvidenceEventIDs))
		for _, eventID := range candidate.EvidenceEventIDs {
			evidence = append(evidence, ports.MemoryEvidenceRecord{
				MemoryID:     id,
				RawEventID:   eventID,
				EvidenceRole: "supports",
				Confidence:   candidate.Confidence,
				Attribution:  "inferred",
			})
		}
		records = append(records, ports.CandidateMemoryRecord{Node: node, Evidence: evidence})
	}
	if len(records) == 0 {
		return nil
	}
	return d.persistMemoryRecords(ctx, job, records)
}

// captureDeterministicCandidate records a memory that an event already states
// (an explicit remember or a session-boundary summary) when the configured
// analyst proposed nothing. It never infers, so it is safe to run even when the
// event was sent to a model that is disabled, unreachable, or degraded.
func (d *JobDispatcher) captureDeterministicCandidate(event observation.Event, result *ports.AnalystResult) {
	if len(result.Candidates) > 0 {
		return
	}
	candidate, ok := analysis.DeterministicCandidate(ports.AnalysisEvent{
		EventID:     event.ID,
		SessionID:   event.SessionID,
		OccurredAt:  event.OccurredAt,
		MessageType: event.MessageType,
		Payload:     event.Payload,
	})
	if !ok {
		return
	}
	result.Candidates = append(result.Candidates, candidate)
}

// candidateStatus decides the initial governance state for a model-proposed
// memory. Session working memory is queried during the same session, so it is
// active immediately; the candidate gate exists to protect the background tree,
// where an inferred memory must be reviewed before it counts as stable.
func candidateStatus(scopeType string) (string, bool) {
	if scopeType == "session" {
		return "active", true
	}
	return "candidate", false
}

func candidateContentText(content json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(content, &fields) != nil {
		return ""
	}
	for _, key := range []string{"text", "content", "summary", "message"} {
		if value, ok := fields[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
