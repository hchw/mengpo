package recall

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

const (
	DefaultMaxRankedCandidates = 20
	recencyHalfLifeDays        = 30.0
)

// RankingContext carries the authorization and applicability inputs for one
// ranking pass. SessionID is the caller's current session: session-scope
// memories from other sessions are excluded as cross-session violations.
type RankingContext struct {
	SessionID string
	// ContextTags drive exact applicability matching (conditions, exclusions,
	// attributes). Missing tags simply fail condition matching.
	ContextTags map[string]string
	// IncludeCandidates allows low-trust exploration mode to surface candidate
	// status memories as explicitly annotated uncertain candidates.
	IncludeCandidates bool
	MaxCandidates     int
	Now               time.Time
}

// RankReason is the explainable score breakdown kept on every ranked
// candidate so projection events can explain ordering later.
type RankReason struct {
	Relevance        float64  `json:"relevance"`
	Confidence       float64  `json:"confidence"`
	Coverage         float64  `json:"coverage"`
	Freshness        float64  `json:"freshness"`
	ApplicabilityHit bool     `json:"applicability_hit"`
	RerankerScore    *float64 `json:"reranker_score,omitempty"`
	Channels         []string `json:"channels"`
}

// RankedCandidate is one explainable ranking outcome. Excluded candidates
// keep their ExcludedReason instead of silently disappearing.
type RankedCandidate struct {
	ports.RecallCandidate
	Reason         RankReason
	FinalScore     float64
	RerankerScore  *float64
	Included       bool
	ExcludedReason string
}

// Rank applies candidate dedup, scope, applicability and conflict filtering,
// then orders survivors by an explainable score. Every channel is only a
// candidate source; no channel score alone decides injection.
func Rank(candidates []ports.RecallCandidate, rankingContext RankingContext) []RankedCandidate {
	if rankingContext.MaxCandidates <= 0 {
		rankingContext.MaxCandidates = DefaultMaxRankedCandidates
	}
	if rankingContext.Now.IsZero() {
		rankingContext.Now = time.Now().UTC()
	}
	deduped := dedupCandidates(candidates)
	ranked := make([]RankedCandidate, 0, len(deduped))
	seen := map[string]bool{}
	for _, candidate := range deduped {
		key := candidate.Node.ID
		if candidate.Node.IdempotencyKey != "" {
			key = candidate.Node.IdempotencyKey
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		ranked = append(ranked, rankOne(candidate, rankingContext))
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Included != ranked[j].Included {
			return ranked[i].Included
		}
		if ranked[i].FinalScore != ranked[j].FinalScore {
			return ranked[i].FinalScore > ranked[j].FinalScore
		}
		return ranked[i].Node.UpdatedAt.After(ranked[j].Node.UpdatedAt)
	})
	// Budget overflow candidates stay in the result, marked excluded with a
	// budget reason, so the projection can explain what was cut.
	for index := rankingContext.MaxCandidates; index < len(ranked); index++ {
		ranked[index].Included = false
		ranked[index].ExcludedReason = "budget"
	}
	return ranked
}

func rankOne(candidate ports.RecallCandidate, rankingContext RankingContext) RankedCandidate {
	result := RankedCandidate{RecallCandidate: candidate, Reason: RankReason{Channels: candidate.Channels}}
	if excluded, reason := exclusionReason(candidate, rankingContext); excluded {
		result.ExcludedReason = reason
		return result
	}
	applicability, applicabilityHit, err := appliesToContext(candidate.Node.Applicability, rankingContext.ContextTags)
	if err != nil || !applicability {
		if err != nil {
			result.ExcludedReason = "applicability-invalid"
		} else {
			result.ExcludedReason = "applicability"
		}
		return result
	}
	result.Reason.ApplicabilityHit = applicabilityHit
	result.Reason.Relevance = clamp01(candidate.Score)
	result.Reason.Confidence = clamp01(candidate.Node.Confidence)
	result.Reason.Coverage = clamp01(float64(len(candidate.Channels)) / 3.0)
	result.Reason.Freshness = freshness(candidate.Node.UpdatedAt, rankingContext.Now)
	result.FinalScore = 0.5*result.Reason.Relevance + 0.3*result.Reason.Confidence + 0.1*result.Reason.Coverage + 0.1*result.Reason.Freshness
	if applicabilityHit {
		result.FinalScore += 0.05
	}
	result.Included = true
	return result
}

func exclusionReason(candidate ports.RecallCandidate, rankingContext RankingContext) (bool, string) {
	node := candidate.Node
	if node.DeletedAt != nil {
		return true, "deleted"
	}
	if node.ExpiresAt != nil && !node.ExpiresAt.After(rankingContext.Now) {
		return true, "expired"
	}
	switch memory.MemoryStatus(node.Status) {
	case memory.StatusActive, memory.StatusStable:
	case memory.StatusCandidate:
		if !rankingContext.IncludeCandidates {
			return true, "status-candidate"
		}
	default:
		// conflicted, rejected, expired status: conflict and rejection close
		// default retrieval.
		return true, "status-" + node.Status
	}
	if !node.DefaultRetrieval {
		return true, "default-retrieval-off"
	}
	if memory.ScopeType(node.ScopeType) == memory.ScopeSession && (rankingContext.SessionID == "" || node.SessionID != rankingContext.SessionID) {
		return true, "cross-session"
	}
	if memory.ScopeType(node.ScopeType) != memory.ScopeUserGlobal && memory.ScopeType(node.ScopeType) != memory.ScopeSession {
		return true, "scope"
	}
	return false, ""
}

func appliesToContext(raw json.RawMessage, contextTags map[string]string) (applicable, tagged bool, err error) {
	if len(raw) == 0 {
		// No applicability constraints: matches every context.
		return true, false, nil
	}
	var applicability memory.Applicability
	if err := json.Unmarshal(raw, &applicability); err != nil {
		return false, false, fmt.Errorf("unmarshal applicability: %w", err)
	}
	if len(applicability.Conditions) == 0 && len(applicability.Exclusions) == 0 && len(applicability.Attributes) == 0 {
		return true, false, nil
	}
	return applicability.AppliesTo(contextTags), true, nil
}

func freshness(updatedAt, now time.Time) float64 {
	if updatedAt.IsZero() || !updatedAt.Before(now) {
		return 1
	}
	ageDays := now.Sub(updatedAt).Hours() / 24
	if ageDays <= 0 {
		return 1
	}
	return clamp01(1 / (1 + ageDays/recencyHalfLifeDays))
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func dedupCandidates(candidates []ports.RecallCandidate) []ports.RecallCandidate {
	byKey := map[string]*ports.RecallCandidate{}
	order := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.Node.ID
		if candidate.Node.IdempotencyKey != "" {
			key = candidate.Node.IdempotencyKey
		}
		if existing, ok := byKey[key]; ok {
			if candidate.Score > existing.Score {
				existing.Score = candidate.Score
			}
			for _, channel := range candidate.Channels {
				known := false
				for _, existingChannel := range existing.Channels {
					if existingChannel == channel {
						known = true
						break
					}
				}
				if !known {
					existing.Channels = append(existing.Channels, channel)
				}
			}
			continue
		}
		copy := candidate
		byKey[key] = &copy
		order = append(order, key)
	}
	deduped := make([]ports.RecallCandidate, 0, len(order))
	for _, key := range order {
		deduped = append(deduped, *byKey[key])
	}
	return deduped
}
