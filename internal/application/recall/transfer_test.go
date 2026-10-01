package recall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

type fakeReranker struct {
	verdicts map[string]float64
	err      error
	metadata ports.RerankerMetadata
}

func (f *fakeReranker) Metadata() ports.RerankerMetadata { return f.metadata }
func (f *fakeReranker) Rank(ctx context.Context, query string, candidates []ports.RerankCandidate) ([]ports.RerankResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	results := make([]ports.RerankResult, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, ports.RerankResult{ID: candidate.ID, Relevance: f.verdicts[candidate.ID]})
	}
	return results, nil
}

func transferSessionNode(id, status string, confidence float64) ports.MemoryNodeRecord {
	return ports.MemoryNodeRecord{ID: id, UserID: "user-1", ScopeType: "session", Status: status, Confidence: confidence, ContentText: "会话中的稳定经验", DefaultRetrieval: true}
}
func transferTargetNode(id string) ports.MemoryNodeRecord {
	return ports.MemoryNodeRecord{ID: id, UserID: "user-1", ScopeType: "user-global", Status: "stable", Confidence: 0.9, ContentText: "用户长期经验"}
}

// TestTransferRegressionSet is the 错误迁移回归集: each bad transfer that
// vector similarity alone would allow must remain explicitly unconfirmed.
func TestTransferRegressionSet(t *testing.T) {
	type regression struct {
		name                      string
		similarity, rerankerScore float64
		rerankerErr               error
		status                    string
		confidence                float64
		evidenceIDs               []string
		wantConfirmed             bool
		wantReasonPart            string
	}
	regressions := []regression{
		{name: "similarity only", similarity: .98, rerankerScore: .1, status: "stable", confidence: .9, evidenceIDs: []string{"e1"}, wantReasonPart: "reranker relevance"},
		{name: "low similarity", similarity: .6, rerankerScore: .95, status: "stable", confidence: .9, evidenceIDs: []string{"e1"}, wantReasonPart: "similarity 0.600 below threshold"},
		{name: "candidate without governance", similarity: .95, rerankerScore: .95, status: "candidate", confidence: .3, evidenceIDs: []string{"e1"}, wantReasonPart: "not active/stable"},
		{name: "missing validated evidence", similarity: .95, rerankerScore: .95, status: "stable", confidence: .9, wantReasonPart: "no validated evidence IDs"},
		{name: "reranker failure", similarity: .95, rerankerErr: errors.New("provider down"), status: "stable", confidence: .9, evidenceIDs: []string{"e1"}, wantReasonPart: "reranker error"},
		{name: "confirmed", similarity: .9, rerankerScore: .9, status: "stable", confidence: .9, evidenceIDs: []string{"e1"}, wantConfirmed: true},
	}
	for _, item := range regressions {
		item := item
		t.Run(item.name, func(t *testing.T) {
			reranker := &fakeReranker{verdicts: map[string]float64{"s1": item.rerankerScore}, err: item.rerankerErr}
			service, err := NewTransferService(reranker, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			proposal, err := service.ProposeTransfer(context.Background(), "长期经验", transferSessionNode("s1", item.status, item.confidence), transferTargetNode("g1"), item.similarity, item.evidenceIDs)
			if err != nil {
				t.Fatalf("ProposeTransfer(): %v", err)
			}
			if proposal.Confirmed != item.wantConfirmed {
				t.Fatalf("confirmed=%v want=%v reasons=%v", proposal.Confirmed, item.wantConfirmed, proposal.RejectedReasons)
			}
			if item.wantConfirmed {
				if len(proposal.Reasons) != 3 {
					t.Fatalf("confirmed reasons=%v want 3 gates", proposal.Reasons)
				}
				return
			}
			found := false
			for _, reason := range proposal.RejectedReasons {
				if strings.Contains(reason, item.wantReasonPart) {
					found = true
				}
			}
			if !found {
				t.Fatalf("reasons=%v want one containing %q", proposal.RejectedReasons, item.wantReasonPart)
			}
		})
	}
}

func TestTransferServiceConstructionAndValidation(t *testing.T) {
	if _, err := NewTransferService(nil, 0, 0); !errors.Is(err, ErrNoReranker) {
		t.Fatalf("nil reranker error=%v", err)
	}
	service, err := NewTransferService(&fakeReranker{verdicts: map[string]float64{}}, .8, .4)
	if err != nil {
		t.Fatal(err)
	}
	if service.similarityThreshold != .8 || service.rerankerThreshold != .4 {
		t.Fatalf("thresholds=%f/%f", service.similarityThreshold, service.rerankerThreshold)
	}
	session, target := transferSessionNode("s1", "stable", .9), transferTargetNode("g1")
	if _, err := service.ProposeTransfer(context.Background(), "q", session, session, .99, []string{"e1"}); err == nil {
		t.Fatal("self-transfer accepted")
	}
	if _, err := service.ProposeTransfer(context.Background(), "q", ports.MemoryNodeRecord{}, target, .99, []string{"e1"}); err == nil {
		t.Fatal("empty session accepted")
	}
	wrongScope := session
	wrongScope.ScopeType = "user-global"
	if _, err := service.ProposeTransfer(context.Background(), "q", wrongScope, target, .99, []string{"e1"}); err == nil {
		t.Fatal("wrong source scope accepted")
	}
	wrongOwner := target
	wrongOwner.UserID = "another-user"
	if _, err := service.ProposeTransfer(context.Background(), "q", session, wrongOwner, .99, []string{"e1"}); err == nil {
		t.Fatal("cross-user transfer accepted")
	}
	service, err = NewTransferService(&fakeReranker{verdicts: map[string]float64{"s1": 1}}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := service.ProposeTransfer(context.Background(), "q", session, target, .1, []string{"e1"})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Confirmed || fmt.Sprint(proposal.RejectedReasons) == "[]" {
		t.Fatal("low similarity transfer accepted or unexplained")
	}
}
