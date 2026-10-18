package assembly

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeTriageGroups struct {
	members []ports.DuplicateGroupMember
}

func (f fakeTriageGroups) LatestCandidateTriage(context.Context, string, []string) (map[string]ports.CandidateTriageRecord, error) {
	return map[string]ports.CandidateTriageRecord{}, nil
}

func (f fakeTriageGroups) DuplicateGroupMembers(context.Context, string, string) ([]ports.DuplicateGroupMember, error) {
	return f.members, nil
}

// TestCandidateDuplicatesReturnsGroupMembers proves the review surface can turn
// an opaque duplicate group id into the memories it actually contains.
func TestCandidateDuplicatesReturnsGroupMembers(t *testing.T) {
	api := &ConsoleAPI{Triage: fakeTriageGroups{members: []ports.DuplicateGroupMember{
		{MemoryID: "m1", Status: "candidate", ScopeType: "user-global", Confidence: 0.9, Content: "prefers a dark terminal"},
		{MemoryID: "m2", Status: "candidate", ScopeType: "user-global", Confidence: 0.8, Content: "likes a dark theme"},
	}}}
	body := consoleEnvelope("tenant-a", "user-1", "req-dup", map[string]any{"group_id": "group-42"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/candidates/duplicates", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	identity := agentaccess.Identity{TenantID: "tenant-a", UserID: "user-1", SourceID: "user-1", Source: agentaccess.SourceVerifiedUser}
	recorder := httptest.NewRecorder()
	api.handleCandidateDuplicates(recorder, request.WithContext(agentaccess.WithIdentity(request.Context(), identity)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items := decoded["data"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v, want 2 members", items)
	}
	first := items[0].(map[string]any)
	if first["id"] != "m1" || first["content"] != "prefers a dark terminal" {
		t.Fatalf("first member = %#v", first)
	}
}

// TestCandidateDuplicatesRequiresGroupID rejects an empty group so the console
// cannot accidentally fetch an unbounded list.
func TestCandidateDuplicatesRequiresGroupID(t *testing.T) {
	api := &ConsoleAPI{Triage: fakeTriageGroups{}}
	body := consoleEnvelope("tenant-a", "user-1", "req-dup", map[string]any{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/candidates/duplicates", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	identity := agentaccess.Identity{TenantID: "tenant-a", UserID: "user-1", SourceID: "user-1", Source: agentaccess.SourceVerifiedUser}
	recorder := httptest.NewRecorder()
	api.handleCandidateDuplicates(recorder, request.WithContext(agentaccess.WithIdentity(request.Context(), identity)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}
