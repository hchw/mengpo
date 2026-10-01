package projection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

type Persistence struct{ repository ports.ProjectionRepository }

func NewPersistence(repository ports.ProjectionRepository) *Persistence {
	return &Persistence{repository: repository}
}

type selectionReason struct {
	Score            float64  `json:"score"`
	Relevance        float64  `json:"relevance"`
	Confidence       float64  `json:"confidence"`
	Coverage         float64  `json:"coverage"`
	Freshness        float64  `json:"freshness"`
	ApplicabilityHit bool     `json:"applicability_hit"`
	RerankerScore    *float64 `json:"reranker_score,omitempty"`
	Channels         []string `json:"channels"`
}
type exclusionReason struct {
	Reason   string   `json:"reason"`
	Channels []string `json:"channels,omitempty"`
}

func (p *Persistence) Record(ctx context.Context, tenantID, requestID, userID, sessionID string, decision Decision, ranked []recall.RankedCandidate, budgetResult BudgetResult, degradedMode string) error {
	selected := make([]string, 0, len(budgetResult.Selected))
	selection := map[string]selectionReason{}
	provenance := map[string][]string{}
	for _, item := range budgetResult.Selected {
		id := item.Candidate.Node.ID
		selected = append(selected, id)
		selection[id] = selectionReason{Score: item.Candidate.FinalScore, Relevance: item.Candidate.Reason.Relevance, Confidence: item.Candidate.Reason.Confidence, Coverage: item.Candidate.Reason.Coverage, Freshness: item.Candidate.Reason.Freshness, ApplicabilityHit: item.Candidate.Reason.ApplicabilityHit, RerankerScore: item.Candidate.RerankerScore, Channels: item.Candidate.Channels}
		provenance[id] = append([]string(nil), item.Candidate.Channels...)
	}
	excluded := map[string]exclusionReason{}
	for _, item := range budgetResult.Excluded {
		id := item.Candidate.Node.ID
		reason := item.ExcludedReason
		if reason == "" {
			reason = item.Candidate.ExcludedReason
		}
		if reason == "" {
			reason = "excluded"
		}
		excluded[id] = exclusionReason{Reason: reason, Channels: append([]string(nil), item.Candidate.Channels...)}
		provenance[id] = append([]string(nil), item.Candidate.Channels...)
	}
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	excludedJSON, err := json.Marshal(excluded)
	if err != nil {
		return err
	}
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return err
	}
	budgetJSON, err := json.Marshal(struct {
		Limits CandidateScope `json:"limits"`
		Usage  BudgetUsage    `json:"usage"`
	}{Limits: decision.Scope, Usage: budgetResult.Usage})
	if err != nil {
		return err
	}
	id, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate projection event id: %w", err)
	}
	mode := string(decision.Mode)
	if mode != "focus" && mode != "diverge" {
		return ports.ErrInvalidProjectionRecord
	}
	return p.repository.RecordProjection(ctx, tenantID, ports.ProjectionEvent{ID: id, RequestID: requestID, UserID: userID, SessionID: sessionID, Mode: mode, SelectedIDs: selected, SelectionReasons: selectionJSON, ExcludedReasons: excludedJSON, Budget: budgetJSON, Provenance: provenanceJSON, DegradedMode: degradedMode})
}

// CacheKey binds a projection result to tenant, user, session scope, query,
// retrieval mode, model versions, and effective budget. QueryHash should be a
// digest rather than raw query text to avoid placing sensitive prompts in keys.
func CacheKey(tenantID, userID, sessionID, scopeType, queryHash, mode, modelVersions string, budget Budget) string {
	material := strings.Join([]string{tenantID, userID, sessionID, scopeType, queryHash, mode, modelVersions, fmt.Sprint(budget.CandidateLimit), fmt.Sprint(budget.RankingLimit), fmt.Sprint(budget.InjectionTokenLimit)}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func (p *Persistence) GetCache(ctx context.Context, tenantID, cacheKey, userID, sessionID, scopeType string) (json.RawMessage, bool, error) {
	return p.repository.GetProjectionCache(ctx, tenantID, cacheKey, userID, sessionID, scopeType)
}
func (p *Persistence) PutCache(ctx context.Context, tenantID, cacheKey, userID, sessionID, scopeType string, response json.RawMessage, ttl time.Duration) error {
	if ttl <= 0 {
		return ports.ErrInvalidProjectionRecord
	}
	return p.repository.PutProjectionCache(ctx, tenantID, ports.ProjectionCacheEntry{CacheKey: cacheKey, UserID: userID, SessionID: sessionID, ScopeType: scopeType, Response: response, ExpiresAt: time.Now().UTC().Add(ttl)})
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
