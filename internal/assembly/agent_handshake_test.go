package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/agents"
	"github.com/hchw/mengpo/internal/domain/auth"
)

type fakeAgentRepository struct {
	agent      auth.Agent
	credential auth.AgentCredential
}

func (f *fakeAgentRepository) CreateAgent(context.Context, auth.Agent, auth.AgentCredential) error {
	return nil
}
func (f *fakeAgentRepository) RotateCredential(context.Context, string, auth.AgentCredential) error {
	return nil
}
func (f *fakeAgentRepository) DisableAgent(context.Context, string) error { return nil }
func (f *fakeAgentRepository) FindByCredentialHash(_ context.Context, hash []byte) (auth.Agent, auth.AgentCredential, error) {
	if len(hash) != len(f.credential.SecretHash) || string(hash) != string(f.credential.SecretHash) {
		return auth.Agent{}, auth.AgentCredential{}, errors.New("credential not found")
	}
	return f.agent, f.credential, nil
}

func secretHash(secret string) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}

func handshakeTestAgent(userIDs []string, capabilities []string) auth.Agent {
	now := time.Now()
	return auth.Agent{
		ID:                "agent-1",
		TenantID:          "tenant-1",
		Name:              "observer",
		AllowedUserIDs:    userIDs,
		AllowedSessionIDs: []string{"session-1"},
		AllowedScopes:     []string{"user-global", "session"},
		Capabilities:      capabilities,
		Status:            auth.AgentActive,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func handshakeTestHandler(t *testing.T, agent auth.Agent, secret string) http.Handler {
	t.Helper()
	repository := &fakeAgentRepository{
		agent:      agent,
		credential: auth.AgentCredential{ID: "cred_agent-1", AgentID: agent.ID, TenantID: agent.TenantID, SecretHash: secretHash(secret)},
	}
	api := &AgentHandshakeAPI{Service: agents.NewService(repository), Agents: repository}
	mux := http.NewServeMux()
	api.Register(mux)
	return mux
}

func doHandshake(t *testing.T, handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/handshake", strings.NewReader(body))
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type handshakeBody struct {
	Version string `json:"version"`
	Data    struct {
		ProtocolVersion     string   `json:"protocol_version"`
		AgentID             string   `json:"agent_id"`
		TenantID            string   `json:"tenant_id"`
		AllowedCapabilities []string `json:"allowed_capabilities"`
		AllowedSessionIDs   []string `json:"allowed_session_ids"`
		AllowedScopeTypes   []string `json:"allowed_scope_types"`
		MaxPayload          int      `json:"max_payload"`
		PrivacyPolicy       string   `json:"privacy_policy"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func decodeHandshake(t *testing.T, recorder *httptest.ResponseRecorder) handshakeBody {
	t.Helper()
	var body handshakeBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode handshake response: %v body=%s", err, recorder.Body.String())
	}
	return body
}

func TestHandshakeRejectsMissingAndInvalidCredential(t *testing.T) {
	handler := handshakeTestHandler(t, handshakeTestAgent([]string{"user-1"}, []string{"observe"}), "secret-1")
	for _, tc := range []struct{ name, secret string }{
		{"missing", ""},
		{"invalid", "wrong-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doHandshake(t, handler, tc.secret, `{"protocol_version":"memory.v1","scope_type":"user-global"}`)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want 401 body=%s", recorder.Code, recorder.Body.String())
			}
			if body := decodeHandshake(t, recorder); body.Error == nil || body.Error.Code != "UNAUTHENTICATED" {
				t.Fatalf("error=%#v", body.Error)
			}
		})
	}
}

func TestHandshakeResolvesSoleBoundUserAndReturnsCapabilities(t *testing.T) {
	agent := handshakeTestAgent([]string{"user-1"}, []string{"observe", "project", "feedback", "session", "consolidate"})
	handler := handshakeTestHandler(t, agent, "secret-1")
	recorder := doHandshake(t, handler, "secret-1", `{"protocol_version":"memory.v1","scope_type":"user-global"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := decodeHandshake(t, recorder)
	if body.Data.TenantID != "tenant-1" || body.Data.AgentID != "agent-1" {
		t.Fatalf("identity leaked or mismatched: %#v", body.Data)
	}
	for _, capability := range []string{"observe", "project", "feedback", "session", "consolidate"} {
		if !containsString(body.Data.AllowedCapabilities, capability) {
			t.Fatalf("missing capability %q in %#v", capability, body.Data.AllowedCapabilities)
		}
	}
	if !containsString(body.Data.AllowedScopeTypes, "session") || !containsString(body.Data.AllowedSessionIDs, "session-1") {
		t.Fatalf("scope not returned: %#v", body.Data)
	}
	if body.Data.MaxPayload != 1<<20 || body.Data.PrivacyPolicy != "tenant-policy" {
		t.Fatalf("handshake metadata = %#v", body.Data)
	}
}

func TestHandshakeRequiresUserForMultiBoundAgent(t *testing.T) {
	agent := handshakeTestAgent([]string{"user-1", "user-2"}, []string{"observe"})
	handler := handshakeTestHandler(t, agent, "secret-1")
	recorder := doHandshake(t, handler, "secret-1", `{"protocol_version":"memory.v1","scope_type":"user-global"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", recorder.Code, recorder.Body.String())
	}
	if body := decodeHandshake(t, recorder); body.Error == nil || body.Error.Code != "USER_REQUIRED" {
		t.Fatalf("error=%#v", body.Error)
	}
	// An explicit, permitted user succeeds.
	ok := doHandshake(t, handler, "secret-1", `{"protocol_version":"memory.v1","scope_type":"user-global","user_id":"user-2"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("declared user status=%d body=%s", ok.Code, ok.Body.String())
	}
	// A user outside the agent's binding is forbidden.
	foreign := doHandshake(t, handler, "secret-1", `{"protocol_version":"memory.v1","scope_type":"user-global","user_id":"user-9"}`)
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign user status=%d body=%s", foreign.Code, foreign.Body.String())
	}
}

func TestHandshakeRejectsUngrantedCapability(t *testing.T) {
	agent := handshakeTestAgent([]string{"user-1"}, []string{"observe", "project"})
	handler := handshakeTestHandler(t, agent, "secret-1")
	recorder := doHandshake(t, handler, "secret-1", `{"protocol_version":"memory.v1","scope_type":"user-global","requested_capabilities":["observe","consolidate"]}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", recorder.Code, recorder.Body.String())
	}
	if body := decodeHandshake(t, recorder); body.Error == nil || body.Error.Code != "INVALID_HANDSHAKE" {
		t.Fatalf("error=%#v", body.Error)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
