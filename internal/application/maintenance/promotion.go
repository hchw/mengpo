package maintenance

import (
	"context"
	"errors"
	"time"

	appgov "github.com/hchw/mengpo/internal/application/governance"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

// PromotionJob promotes candidates that independent evidence supports. It never
// promotes on a model's self-reported confidence: every decision comes from
// EvaluateGovernance over the candidate's verified evidence, and lands through
// the governance service so it is audited and version-checked like any other
// governance action.
//
// Two guards make corroboration meaningful. First, the influence reader drops
// evidence from sessions that used the memory (an echo is not a witness).
// Second, EvaluateGovernance only counts high-reliability source sessions, so an
// unknown-reliability event can never promote anything.
type PromotionJob struct {
	PlanName   string
	Reader     ports.RecentCandidateReader
	Evidence   ports.MemoryEvidenceReader
	Influence  ports.MemoryInfluenceReader
	Governance GovernanceApplier
	Policy     memory.GovernancePolicy
	BatchSize  int
	Runs       ports.AnalysisRunStore
	NewID      func() (string, error)
	Clock      func() time.Time
}

func (j *PromotionJob) Name() string { return j.PlanName }

func (j *PromotionJob) Run(ctx context.Context, tenantID string) error {
	if j.Reader == nil || j.Evidence == nil || j.Governance == nil {
		return errors.New("promotion job is not configured")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	policy := j.Policy
	if policy.Version == "" {
		policy = memory.DefaultGovernancePolicy()
	}
	nodes, err := j.Reader.ListRecentCandidates(ctx, tenantID, batchSize)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}
	now := j.now()
	promoted := 0
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		evidence, err := j.verifiedEvidence(ctx, tenantID, node)
		if err != nil {
			return err
		}
		current, err := appgov.FromRecord(node)
		if err != nil {
			// An unreadable candidate is skipped, never guessed at.
			continue
		}
		decision, err := memory.EvaluateGovernance(current, memory.GovernanceInput{VerifiedEvidence: evidence}, policy)
		if err != nil {
			continue
		}
		if decision.Memory.Status == current.Status {
			continue
		}
		if err := j.activate(ctx, tenantID, node.ID, current.Version, decision, now); err != nil {
			// A version conflict or transient failure is expected and safe to
			// skip: the next window re-evaluates the same candidate.
			continue
		}
		promoted++
	}
	return recordRun(ctx, j.Runs, j.NewID, runSpec{
		tenantID: tenantID, plan: j.PlanName, task: "candidate_promotion", now: now, candidateCount: promoted,
	})
}

// verifiedEvidence loads the candidate's evidence and removes any row whose
// source session was influenced by the memory or its lineage.
func (j *PromotionJob) verifiedEvidence(ctx context.Context, tenantID string, node ports.MemoryNodeRecord) ([]memory.Evidence, error) {
	records, err := j.Evidence.ListMemoryEvidence(ctx, tenantID, node.ID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	excluded := map[string]struct{}{}
	if j.Influence != nil {
		sessions := make([]string, 0, len(records))
		for _, record := range records {
			if record.SourceSessionID != "" {
				sessions = append(sessions, record.SourceSessionID)
			}
		}
		if len(sessions) > 0 {
			excluded, err = j.Influence.InfluencedSessions(ctx, tenantID, node.ID, sessions)
			if err != nil {
				return nil, err
			}
		}
	}
	evidence := make([]memory.Evidence, 0, len(records))
	for _, record := range records {
		if record.SourceSessionID != "" {
			if _, influenced := excluded[record.SourceSessionID]; influenced {
				continue
			}
		}
		evidence = append(evidence, memory.Evidence{
			ID: record.ID, MemoryID: record.MemoryID, RawEventID: record.RawEventID,
			NormalizedEventID: record.NormalizedEventID, SourceSessionID: record.SourceSessionID,
			Role: record.EvidenceRole, Confidence: record.Confidence,
			Reliability: memory.EvidenceReliability(record.Reliability),
			Attribution: memory.Attribution(record.Attribution),
		})
	}
	return evidence, nil
}

func (j *PromotionJob) activate(ctx context.Context, tenantID, memoryID string, expectedVersion int64, decision memory.GovernanceDecision, now time.Time) error {
	auditID, err := j.newID()
	if err != nil {
		return err
	}
	requestID, err := j.newID()
	if err != nil {
		return err
	}
	_, err = j.Governance.Apply(ctx, tenantID, memoryID, memory.GovernanceCommand{
		Action:          memory.GovernanceActivate,
		ExpectedVersion: expectedVersion,
		AuditID:         auditID,
		ActorType:       "system",
		ActorID:         j.PlanName,
		RequestID:       requestID,
		At:              now,
		Promotion: &memory.GovernancePromotion{
			TargetStatus: decision.Memory.Status,
			Confidence:   decision.ConfidenceScore,
			Reason:       decision.Reason,
		},
	})
	return err
}

func (j *PromotionJob) now() time.Time         { return clock(j.Clock) }
func (j *PromotionJob) newID() (string, error) { return newIDOr(j.NewID) }
