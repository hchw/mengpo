package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/ports"
)

// SameFactConfirmer decides whether two memories describe the same fact. It is
// the mandatory pre-merge guard: only an explicit positive confirms, and any
// error means "not confirmed". It fails closed.
type SameFactConfirmer interface {
	ConfirmSameFact(ctx context.Context, batch ports.AnalysisBatch) (bool, error)
}

// NearDuplicateJob consolidates near-duplicate memories in the same scope. It
// merges only when the cluster passes the conflict guard and an explicit
// same-fact confirmation, so a related-but-different memory is never merged.
// Merging is its own audited primitive; the job never changes a candidate's
// governance status directly.
type NearDuplicateJob struct {
	PlanName        string
	Reader          ports.RecentCandidateReader
	Neighbors       ports.SemanticNeighborReader
	Triage          ports.CandidateTriageReader
	Evidence        ports.MemoryEvidenceReader
	Merge           ports.MemoryMergeRepository
	Confirmer       SameFactConfirmer
	Runs            ports.AnalysisRunStore
	PromptVersion   string
	SchemaVersion   string
	BatchSize       int
	Threshold       float64
	MergeEnabled    bool
	LexicalFallback bool
	NewID           func() (string, error)
	Clock           func() time.Time
}

// Name implements ports.ScheduledJob.
func (j *NearDuplicateJob) Name() string { return j.PlanName }

type nearDuplicateReport struct {
	Scanned            int  `json:"scanned"`
	Groups             int  `json:"duplicate_groups"`
	DuplicatePairs     int  `json:"duplicate_pairs"`
	Merged             int  `json:"merged"`
	SkippedConflict    int  `json:"skipped_conflict"`
	SkippedUnconfirmed int  `json:"skipped_unconfirmed"`
	SkippedNoEmbedding int  `json:"skipped_no_embedding"`
	SkippedVersion     int  `json:"skipped_version"`
	MergeEnabled       bool `json:"merge_enabled"`
}

// Run scans recent candidates, clusters near duplicates, and (when merging is
// enabled) confirms and merges them through the audited merge primitive.
func (j *NearDuplicateJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Neighbors == nil || j.Runs == nil {
		return errors.New("near-duplicate job is not configured")
	}
	if j.MergeEnabled && (j.Merge == nil || j.Confirmer == nil || !j.hasConfirmationContext()) {
		return errors.New("near-duplicate merging requires a merge repository, a same-fact confirmer, and prompt/schema versions")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	seeds, err := j.Reader.ListRecentCandidates(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	if len(seeds) == 0 {
		return nil
	}
	now := j.now()
	runID := correlationID(j.PlanName, tenantID, now)

	nodes, adjacency, noEmbedding, err := j.cluster(ctx, tenantID, seeds)
	if err != nil {
		return err
	}
	groups := groupDuplicates(nodes, adjacency)
	members := make(map[string][]ports.MemoryNodeRecord, len(nodes))
	for _, node := range nodes {
		group := groups[node.ID]
		members[group] = append(members[group], node)
	}

	allIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		allIDs = append(allIDs, node.ID)
	}
	conflicts := map[string]ports.CandidateTriageRecord{}
	if j.Triage != nil {
		conflicts, err = j.Triage.LatestCandidateTriage(ctx, tenantID, allIDs)
		if err != nil {
			return err
		}
	}
	evidenceCount := map[string]int{}
	if j.Confirmer != nil && j.Evidence != nil {
		for _, node := range nodes {
			records, err := j.Evidence.ListMemoryEvidence(ctx, tenantID, node.ID)
			if err != nil {
				return err
			}
			evidenceCount[node.ID] = len(records)
		}
	}

	report := nearDuplicateReport{Scanned: len(nodes), SkippedNoEmbedding: noEmbedding, MergeEnabled: j.MergeEnabled}
	// Deterministic iteration order so runs are reproducible.
	groupIDs := make([]string, 0, len(members))
	for group := range members {
		groupIDs = append(groupIDs, group)
	}
	sort.Strings(groupIDs)
	for _, group := range groupIDs {
		clusterNodes := members[group]
		if len(clusterNodes) < 2 {
			continue
		}
		report.Groups++
		report.DuplicatePairs += len(clusterNodes) - 1
		if groupConflicts(clusterNodes, conflicts) {
			report.SkippedConflict++
			continue
		}
		if !j.MergeEnabled {
			continue
		}
		target := chooseMergeTarget(clusterNodes, evidenceCount)
		confirmedIDs := make([]string, 0, len(clusterNodes)-1)
		unconfirmed := 0
		for _, member := range clusterNodes {
			if member.ID == target.ID {
				continue
			}
			confirmed, err := j.confirmSameFact(ctx, tenantID, runID, target, member)
			if err != nil || !confirmed {
				unconfirmed++
				continue
			}
			confirmedIDs = append(confirmedIDs, member.ID)
		}
		if unconfirmed > 0 {
			report.SkippedUnconfirmed += unconfirmed
		}
		if len(confirmedIDs) == 0 {
			continue
		}
		result, err := j.Merge.MergeMemories(ctx, tenantID, ports.MergeRequest{
			TargetID: target.ID, DuplicateIDs: confirmedIDs,
			ActorType: "system", ActorID: j.PlanName, RequestID: runID, At: now,
		})
		if err != nil {
			if errors.Is(err, ports.ErrInvalidMerge) || errors.Is(err, ports.ErrMergeScopeDrift) {
				report.SkippedVersion++
				continue
			}
			return err
		}
		report.Merged += len(result.Duplicates)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return recordRun(ctx, j.Runs, j.newID, runSpec{
		tenantID:       tenantID,
		plan:           j.PlanName,
		task:           "near_duplicate_merge",
		now:            now,
		candidateCount: report.Merged,
		discardedCount: report.SkippedConflict + report.SkippedUnconfirmed + report.SkippedNoEmbedding + report.SkippedVersion,
		result:         encoded,
	})
}

func (j *NearDuplicateJob) hasConfirmationContext() bool {
	return j.PromptVersion != "" && j.SchemaVersion != ""
}

// cluster collects the seeds plus their semantic neighbors. A seed without a
// ready embedding contributes no semantic edges (degradation); with the lexical
// fallback the reader still returns trigram-similar neighbors. The reported
// count is the number of seeds that could not be compared by embedding.
func (j *NearDuplicateJob) cluster(ctx context.Context, tenantID string, seeds []ports.MemoryNodeRecord) ([]ports.MemoryNodeRecord, map[string][]string, int, error) {
	nodes := make(map[string]ports.MemoryNodeRecord, len(seeds))
	order := make([]string, 0, len(seeds))
	add := func(node ports.MemoryNodeRecord) {
		if _, ok := nodes[node.ID]; !ok {
			nodes[node.ID] = node
			order = append(order, node.ID)
		}
	}
	for _, seed := range seeds {
		add(seed)
	}
	adjacency := make(map[string][]string, len(seeds))
	noEmbedding := 0
	limit := len(seeds)
	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		ready, err := j.Neighbors.EmbeddingReady(ctx, tenantID, seed.ID)
		if err != nil {
			return nil, nil, 0, err
		}
		if !ready {
			noEmbedding++
			if !j.LexicalFallback {
				continue
			}
		}
		neighbors, err := j.Neighbors.ListSemanticNeighbors(ctx, tenantID, ports.SemanticNeighborQuery{
			ReferenceMemoryID: seed.ID, Threshold: j.Threshold, Limit: limit, LexicalFallback: j.LexicalFallback,
		})
		if err != nil {
			return nil, nil, 0, err
		}
		for _, neighbor := range neighbors {
			add(neighbor.Node)
			adjacency[seed.ID] = append(adjacency[seed.ID], neighbor.Node.ID)
		}
	}
	full := make([]ports.MemoryNodeRecord, 0, len(order))
	for _, id := range order {
		full = append(full, nodes[id])
	}
	return full, adjacency, noEmbedding, nil
}

func (j *NearDuplicateJob) confirmSameFact(ctx context.Context, tenantID, runID string, target, duplicate ports.MemoryNodeRecord) (bool, error) {
	batch := ports.AnalysisBatch{
		TenantID:      tenantID,
		RunID:         runID,
		TaskType:      observation.TaskConfirmDuplicate,
		PromptVersion: j.PromptVersion,
		SchemaVersion: j.SchemaVersion,
		Candidates: []ports.CandidateMemory{
			candidateForConfirmation(target),
			candidateForConfirmation(duplicate),
		},
	}
	return j.Confirmer.ConfirmSameFact(ctx, batch)
}

func candidateForConfirmation(node ports.MemoryNodeRecord) ports.CandidateMemory {
	content := node.Content
	if len(content) == 0 || !json.Valid(content) {
		encoded, _ := json.Marshal(map[string]string{"text": node.ContentText})
		content = encoded
	}
	return ports.CandidateMemory{
		CandidateID: node.ID, ScopeType: node.ScopeType, ScopeID: node.ScopeID,
		Content: content, Confidence: node.Confidence,
	}
}

// groupConflicts reports whether any member of the cluster is flagged as
// conflicting with another member by the advisory triage.
func groupConflicts(members []ports.MemoryNodeRecord, conflicts map[string]ports.CandidateTriageRecord) bool {
	inGroup := make(map[string]struct{}, len(members))
	for _, member := range members {
		inGroup[member.ID] = struct{}{}
	}
	for _, member := range members {
		for _, other := range conflicts[member.ID].ConflictIDs {
			if _, ok := inGroup[other]; ok {
				return true
			}
		}
	}
	return false
}

// chooseMergeTarget picks the canonical node deterministically: stable over
// active over candidate, then higher confidence, then more evidence, then the
// earliest created, then id.
func chooseMergeTarget(members []ports.MemoryNodeRecord, evidenceCount map[string]int) ports.MemoryNodeRecord {
	sorted := append([]ports.MemoryNodeRecord(nil), members...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if statusRank(a.Status) != statusRank(b.Status) {
			return statusRank(a.Status) < statusRank(b.Status)
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if evidenceCount[a.ID] != evidenceCount[b.ID] {
			return evidenceCount[a.ID] > evidenceCount[b.ID]
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return sorted[0]
}

func statusRank(status string) int {
	switch status {
	case "stable":
		return 0
	case "active":
		return 1
	case "candidate":
		return 2
	default:
		return 3
	}
}

func (j *NearDuplicateJob) now() time.Time         { return clock(j.Clock) }
func (j *NearDuplicateJob) newID() (string, error) { return newIDOr(j.NewID) }
