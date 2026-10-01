package assembly

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/projection"
	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

// Default projection budgets used when the Envelope omits explicit values.
const (
	defaultCandidateBudget      = 20
	defaultRankingBudget        = 20
	defaultInjectionTokenBudget = 2048
)

// ConsolidateJobType is the durable outbox job that carries a consolidation
// request to the worker.
const ConsolidateJobType = "consolidate"

// Projector implements agentaccess.Projector by orchestrating a bounded
// Focus/Divergence decision and running the projection pipeline.
type Projector struct {
	Orchestrator *projection.Orchestrator
	Pipeline     *projection.Pipeline
}

type projectPayload struct {
	Query       string `json:"query"`
	MemoryType  string `json:"memory_type"`
	QueryHash   string `json:"query_hash"`
	NewEvidence bool   `json:"new_evidence"`
}

func (p *Projector) Project(ctx context.Context, scoped agentaccess.Scoped, envelope dto.Envelope) (any, error) {
	if p == nil || p.Orchestrator == nil || p.Pipeline == nil {
		return nil, errors.New("project use case is not configured")
	}
	var payload projectPayload
	if err := decodeStrict(envelope.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.MemoryType != "" && payload.MemoryType != "user-global" && payload.MemoryType != "session" {
		return nil, fmt.Errorf("%w: unsupported memory_type", dto.ErrInvalidEnvelope)
	}
	signals := projection.Signals{
		Clarity:              projection.ClarityClear,
		CandidateBudget:      firstPositive(envelope.Budget.Candidates, defaultCandidateBudget),
		RankingBudget:        firstPositive(envelope.Budget.Ranking, defaultRankingBudget),
		InjectionTokenBudget: firstPositive(envelope.Budget.InjectionTokens, defaultInjectionTokenBudget),
	}
	if envelope.MemoryHint != nil {
		signals.Hint = envelope.MemoryHint.Mode
		signals.HintTopics = envelope.MemoryHint.Topics
		signals.HintMemoryIDs = envelope.MemoryHint.MemoryIDs
	}
	decision, err := p.Orchestrator.Choose(signals)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", dto.ErrInvalidEnvelope, err)
	}
	queryHash := payload.QueryHash
	if queryHash == "" {
		sum := sha256.Sum256([]byte(payload.Query))
		queryHash = hex.EncodeToString(sum[:])
	}
	projection, err := p.Pipeline.Run(ctx, projection.PipelineRequest{
		TenantID:      scoped.TenantID,
		UserID:        scoped.UserID,
		SessionID:     scoped.SessionID,
		Query:         payload.Query,
		QueryHash:     queryHash,
		Recall:        recall.RecallRequest{Query: payload.Query, MemoryType: payload.MemoryType},
		Decision:      decision,
		Budget:        projection.Budget{CandidateLimit: decision.Scope.CandidateLimit, RankingLimit: decision.Scope.RankingLimit, InjectionTokenLimit: decision.Scope.InjectionTokenBudget},
		ModelVersions: "v1",
		ScopeType:     scoped.ScopeType,
		NewEvidence:   payload.NewEvidence,
	})
	if err != nil {
		return nil, err
	}
	return projection, nil
}

// FeedbackRecorder implements agentaccess.FeedbackRecorder by persisting one
// user/agent feedback signal for an injected memory.
type FeedbackRecorder struct {
	Repository ports.FeedbackRepository
}

type feedbackPayload struct {
	MemoryID string `json:"memory_id"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

func (f *FeedbackRecorder) Feedback(ctx context.Context, scoped agentaccess.Scoped, envelope dto.Envelope) (any, error) {
	if f == nil || f.Repository == nil {
		return nil, errors.New("feedback use case is not configured")
	}
	var payload feedbackPayload
	if err := decodeStrict(envelope.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.MemoryID == "" {
		return nil, fmt.Errorf("%w: feedback requires memory_id", dto.ErrInvalidEnvelope)
	}
	if !ports.ValidFeedbackType(payload.Type) {
		return nil, fmt.Errorf("%w: unsupported feedback type %q", dto.ErrInvalidEnvelope, payload.Type)
	}
	if err := f.Repository.StoreFeedback(ctx, scoped.TenantID, ports.MemoryFeedback{
		MemoryID:  payload.MemoryID,
		UserID:    scoped.UserID,
		SessionID: scoped.SessionID,
		Type:      payload.Type,
		Reason:    payload.Reason,
		RequestID: scoped.RequestID,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"stored": true, "memory_id": payload.MemoryID, "type": payload.Type}, nil
}

// Consolidator implements agentaccess.Consolidator. Consolidation is
// asynchronous: it enqueues one durable job and returns a run handle. The
// worker consumes the job; PostgreSQL remains the source of truth.
type Consolidator struct {
	Jobs  ports.OutboxEnqueuer
	NewID func() (string, error)
}

func (c *Consolidator) Consolidate(ctx context.Context, scoped agentaccess.Scoped, envelope dto.Envelope) (any, error) {
	if c == nil || c.Jobs == nil {
		return nil, errors.New("consolidate use case is not configured")
	}
	newID := c.NewID
	if newID == nil {
		newID = newUUID
	}
	runID, err := newID()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"scope_type": scoped.ScopeType,
		"user_id":    scoped.UserID,
		"session_id": scoped.SessionID,
	})
	if err != nil {
		return nil, err
	}
	if err := c.Jobs.EnqueueJob(ctx, ports.OutboxJob{
		ID:             runID,
		TenantID:       scoped.TenantID,
		JobType:        ConsolidateJobType,
		IdempotencyKey: "consolidate:" + scoped.IdempotencyKey,
		Payload:        payload,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"run_id": runID, "accepted": true, "tenant_id": scoped.TenantID}, nil
}

func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: payload decode: %v", dto.ErrInvalidEnvelope, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing payload value", dto.ErrInvalidEnvelope)
	}
	return nil
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return hex.EncodeToString(value[0:4]) + "-" + hex.EncodeToString(value[4:6]) + "-" + hex.EncodeToString(value[6:8]) + "-" + hex.EncodeToString(value[8:10]) + "-" + hex.EncodeToString(value[10:16]), nil
}
