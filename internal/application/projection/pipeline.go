package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/ports"
)

// Retriever is the hybrid recall entry point. *recall.Service satisfies it.
type Retriever interface {
	RecallWithMeta(ctx context.Context, tenantID, userID, sessionID string, request recall.RecallRequest) ([]ports.RecallCandidate, recall.RecallMeta, error)
}

// PipelinePolicy controls degradation behavior. Degradation never silently
// drops context: every fallback records an explicit reason.
type PipelinePolicy struct {
	Timeout                time.Duration
	CacheTTL               time.Duration
	DegradeOnDatabaseError bool
	RankingContext         recall.RankingContext
}

type PipelineRequest struct {
	TenantID, UserID, SessionID string
	Query                       string
	QueryHash                   string
	Recall                      recall.RecallRequest
	Decision                    Decision
	Budget                      Budget
	ModelVersions               string
	ScopeType                   string
	// NewEvidence forces a cache bypass when the caller knows the memory
	// watermark advanced.
	NewEvidence bool
}

type ProjectionMetadata struct {
	Mode            string   `json:"mode"`
	Reason          string   `json:"reason"`
	CacheHit        bool     `json:"cache_hit"`
	Degraded        bool     `json:"degraded"`
	DegradedReasons []string `json:"degraded_reasons"`
	ChannelsUsed    []string `json:"channels_used"`
}

type Projection struct {
	Items        []Item             `json:"items"`
	Usage        BudgetUsage        `json:"usage"`
	Metadata     ProjectionMetadata `json:"metadata"`
	ProjectionID string             `json:"projection_id,omitempty"`
}

type cachedProjection struct {
	Items        []Item      `json:"items"`
	Usage        BudgetUsage `json:"usage"`
	ProjectionID string      `json:"projection_id,omitempty"`
}

// Pipeline is the projection use case: it composes orchestration decision,
// retrieval, ranking, budgets, cache and degradation. If retrieval times out
// or the database is unavailable it degrades to local session context rather
// than failing the caller.
type Pipeline struct {
	policy         PipelinePolicy
	retriever      Retriever
	sessionContext ports.SessionContextProvider
	persistence    *Persistence
	budgets        *BudgetManager
}

func NewPipeline(policy PipelinePolicy, retriever Retriever, sessionContext ports.SessionContextProvider, persistence *Persistence) *Pipeline {
	if policy.Timeout <= 0 {
		policy.Timeout = 2 * time.Second
	}
	return &Pipeline{policy: policy, retriever: retriever, sessionContext: sessionContext, persistence: persistence, budgets: NewBudgetManager()}
}

func (p *Pipeline) Run(ctx context.Context, request PipelineRequest) (Projection, error) {
	if request.TenantID == "" || request.UserID == "" || request.QueryHash == "" {
		return Projection{}, fmt.Errorf("%w: projection requires tenant, user, query hash", ports.ErrInvalidProjectionRecord)
	}
	if request.Budget.CandidateLimit < 1 || request.Budget.RankingLimit < 1 || request.Budget.InjectionTokenLimit < 1 {
		return Projection{}, ErrInvalidBudget
	}
	mode := string(request.Decision.Mode)
	if mode != "focus" && mode != "diverge" {
		return Projection{}, fmt.Errorf("%w: projection mode", ports.ErrInvalidProjectionRecord)
	}
	if p.retriever == nil {
		return Projection{}, errors.New("projection pipeline requires a retriever")
	}
	metadata := ProjectionMetadata{Mode: mode, Reason: request.Decision.Reason, DegradedReasons: []string{}}

	cacheKey := ""
	if p.persistence != nil && p.policy.CacheTTL > 0 {
		cacheKey = CacheKey(request.TenantID, request.UserID, request.SessionID, request.ScopeType, request.QueryHash, mode, request.ModelVersions, request.Budget)
		if request.NewEvidence {
			metadata.DegradedReasons = append(metadata.DegradedReasons, "cache-invalidated: new-evidence")
		} else if cached, ok, err := p.persistence.GetCache(ctx, request.TenantID, cacheKey, request.UserID, request.SessionID, request.ScopeType); err != nil {
			metadata.DegradedReasons = append(metadata.DegradedReasons, "cache-read-failed: "+err.Error())
		} else if ok {
			var stored cachedProjection
			if err := json.Unmarshal(cached, &stored); err != nil {
				metadata.DegradedReasons = append(metadata.DegradedReasons, "cache-decode-failed: "+err.Error())
			} else {
				metadata.CacheHit = true
				// A cache hit returns the identity of the projection that produced this
				// result, so the caller can still reference what it actually received.
				return Projection{Items: stored.Items, Usage: stored.Usage, Metadata: metadata, ProjectionID: stored.ProjectionID}, nil
			}
		}
	}

	recallCtx, cancel := context.WithTimeout(ctx, p.policy.Timeout)
	defer cancel()
	candidates, recallMeta, err := p.retriever.RecallWithMeta(recallCtx, request.TenantID, request.UserID, request.SessionID, request.Recall)
	metadata.ChannelsUsed = recallMeta.ChannelsUsed
	if err != nil {
		timeout := errors.Is(err, context.DeadlineExceeded) || errors.Is(recallCtx.Err(), context.DeadlineExceeded)
		if timeout {
			return p.degradeToSessionContext(ctx, request, metadata, "retrieval-timeout")
		}
		if p.policy.DegradeOnDatabaseError {
			return p.degradeToSessionContext(ctx, request, metadata, "retrieval-database-unavailable: "+err.Error())
		}
		return Projection{}, fmt.Errorf("retrieval failed: %w", err)
	}
	// Embedding and reranker degradation are surfaced by recall; keep them on
	// the projection metadata so callers know retrieval was partial.
	metadata.DegradedReasons = append(metadata.DegradedReasons, recallMeta.Degraded...)

	rankingContext := p.policy.RankingContext
	rankingContext.IncludeCandidates = request.Decision.Scope.IncludeCandidates
	if rankingContext.MaxCandidates <= 0 {
		rankingContext.MaxCandidates = request.Budget.RankingLimit
	}
	ranked := recall.Rank(candidates, rankingContext)
	selected, err := p.budgets.Select(ranked, request.Budget)
	if err != nil {
		return Projection{}, err
	}
	projection := Projection{Items: selected.Selected, Usage: selected.Usage, Metadata: metadata}
	projection.Metadata.Degraded = len(projection.Metadata.DegradedReasons) > 0

	if p.persistence != nil {
		degradedMode := ""
		if projection.Metadata.Degraded {
			degradedMode = "degraded"
		}
		projectionID, recordErr := p.persistence.Record(ctx, request.TenantID, request.QueryHash, request.UserID, request.SessionID, request.Decision, ranked, selected, degradedMode)
		if recordErr != nil {
			projection.Metadata.DegradedReasons = append(projection.Metadata.DegradedReasons, "projection-record-failed: "+recordErr.Error())
			projection.Metadata.Degraded = true
		} else {
			projection.ProjectionID = projectionID
		}
		if cacheKey != "" {
			if payload, marshalErr := json.Marshal(cachedProjection{Items: selected.Selected, Usage: selected.Usage, ProjectionID: projectionID}); marshalErr == nil {
				if err := p.persistence.PutCache(ctx, request.TenantID, cacheKey, request.UserID, request.SessionID, request.ScopeType, payload, p.policy.CacheTTL); err != nil {
					projection.Metadata.DegradedReasons = append(projection.Metadata.DegradedReasons, "cache-write-failed: "+err.Error())
					projection.Metadata.Degraded = true
				}
			}
		}
	}
	return projection, nil
}

func (p *Pipeline) degradeToSessionContext(ctx context.Context, request PipelineRequest, metadata ProjectionMetadata, reason string) (Projection, error) {
	metadata.Degraded = true
	metadata.DegradedReasons = append(metadata.DegradedReasons, reason)
	projection := Projection{Items: []Item{}, Metadata: metadata}
	if p.sessionContext == nil {
		return projection, nil
	}
	items, err := p.sessionContext.LocalContext(ctx, request.TenantID, request.UserID, request.SessionID)
	if err != nil {
		metadata.DegradedReasons = append(metadata.DegradedReasons, "session-context-unavailable: "+err.Error())
		projection.Metadata = metadata
		return projection, nil
	}
	remaining := request.Budget.InjectionTokenLimit
	for _, item := range items {
		if remaining <= 0 {
			break
		}
		text := item.Text
		if estimateTokens(text) == 0 {
			continue
		}
		if estimateTokens(text) > remaining {
			text = truncateToTokens(text, remaining)
		}
		cost := estimateTokens(text)
		projection.Items = append(projection.Items, Item{Text: text, TokenCost: cost})
		projection.Usage.CandidatesInjected++
		projection.Usage.TokensInjected += cost
		remaining -= cost
	}
	return projection, nil
}
