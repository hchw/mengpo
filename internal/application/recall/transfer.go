package recall

import (
	"context"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/ports"
)

const (
	DefaultTransferSimilarityThreshold = 0.85
	DefaultTransferRerankerThreshold   = 0.5
)

// TransferCandidate proposes promoting a session-scope memory into a
// user-global counterpart. Vector similarity is only a candidate-generation
// signal; independent cross-encoder relevance, validated evidence IDs, and
// governance-ready source state are all required for confirmation.
type TransferCandidate struct {
	SessionMemory     ports.MemoryNodeRecord
	TargetMemoryID    string
	Similarity        float64
	RerankerRelevance float64
	EvidenceIDs       []string
	Reasons           []string
	Confirmed         bool
	RejectedReasons   []string
}

type TransferService struct {
	reranker            ports.Reranker
	similarityThreshold float64
	rerankerThreshold   float64
}

func NewTransferService(reranker ports.Reranker, similarityThreshold, rerankerThreshold float64) (*TransferService, error) {
	if reranker == nil {
		return nil, ErrNoReranker
	}
	if similarityThreshold <= 0 {
		similarityThreshold = DefaultTransferSimilarityThreshold
	}
	if rerankerThreshold <= 0 {
		rerankerThreshold = DefaultTransferRerankerThreshold
	}
	return &TransferService{reranker: reranker, similarityThreshold: similarityThreshold, rerankerThreshold: rerankerThreshold}, nil
}

// ProposeTransfer gates session-to-user-global transfer on similarity,
// independent reranker relevance, evidence references and governance state.
// The caller must source evidenceIDs from validated tenant-scoped evidence;
// this returns only a proposal and never applies a governance mutation.
func (s *TransferService) ProposeTransfer(ctx context.Context, query string, session ports.MemoryNodeRecord, target ports.MemoryNodeRecord, similarity float64, evidenceIDs []string) (TransferCandidate, error) {
	if session.ID == "" || target.ID == "" || session.ID == target.ID || session.ScopeType != "session" || target.ScopeType != "user-global" || session.UserID == "" || session.UserID != target.UserID {
		return TransferCandidate{}, ports.ErrInvalidRerankRequest
	}
	proposal := TransferCandidate{
		SessionMemory: session, TargetMemoryID: target.ID, Similarity: similarity,
		EvidenceIDs: append([]string(nil), evidenceIDs...),
	}
	if similarity < s.similarityThreshold {
		proposal.RejectedReasons = append(proposal.RejectedReasons,
			fmt.Sprintf("similarity %.3f below threshold %.3f", similarity, s.similarityThreshold))
	}
	if query == "" || session.ContentText == "" || target.ContentText == "" {
		proposal.RejectedReasons = append(proposal.RejectedReasons, "query or transfer memory text is empty")
	} else {
		// Put target context into the ranking query so the independent model
		// judges whether the session item supports the user-global counterpart.
		rerankQuery := query + "\nExisting user-global memory: " + target.ContentText
		results, err := s.reranker.Rank(ctx, rerankQuery, []ports.RerankCandidate{{ID: session.ID, Text: session.ContentText}})
		if err != nil {
			proposal.RejectedReasons = append(proposal.RejectedReasons, "reranker error: "+err.Error())
		} else if len(results) != 1 || results[0].ID != session.ID {
			proposal.RejectedReasons = append(proposal.RejectedReasons, "reranker returned no valid verdict")
		} else {
			proposal.RerankerRelevance = results[0].Relevance
			if results[0].Relevance < s.rerankerThreshold {
				proposal.RejectedReasons = append(proposal.RejectedReasons,
					fmt.Sprintf("reranker relevance %.3f below threshold %.3f", results[0].Relevance, s.rerankerThreshold))
			}
		}
	}
	if !hasTransferEvidence(session) {
		proposal.RejectedReasons = append(proposal.RejectedReasons, "session memory is not active/stable with sufficient confidence")
	}
	if len(proposal.EvidenceIDs) == 0 {
		proposal.RejectedReasons = append(proposal.RejectedReasons, "no validated evidence IDs supplied")
	}
	if len(proposal.RejectedReasons) == 0 {
		proposal.Confirmed = true
		proposal.Reasons = []string{
			fmt.Sprintf("vector similarity %.3f >= %.3f", similarity, s.similarityThreshold),
			fmt.Sprintf("cross-encoder relevance %.3f >= %.3f", proposal.RerankerRelevance, s.rerankerThreshold),
			fmt.Sprintf("%d validated evidence IDs supplied", len(proposal.EvidenceIDs)),
		}
	}
	return proposal, nil
}

func hasTransferEvidence(session ports.MemoryNodeRecord) bool {
	switch session.Status {
	case "active", "stable":
	default:
		return false
	}
	return session.Confidence >= 0.5 && session.DefaultRetrieval
}

var ErrNoReranker = errors.New("transfer service requires an independent reranker")
