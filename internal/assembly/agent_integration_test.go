package assembly

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestAgentRegistrationAndBearerAuth exercises the full agent lifecycle: an
// admin registers an agent over the console session, then the returned secret
// authenticates a bound user through a Bearer credential without any session.
func TestAgentRegistrationAndBearerAuth(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	const email = "agent-owner@example.com"
	t.Cleanup(func() {
		dropUserTenants(context.Background(), t, db, email)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.users WHERE email=$1`, email)
	})

	cfg := config.Config{Environment: "development", Auth: config.AuthConfig{SessionCookieName: "memory_session", SessionTTL: time.Hour}, Security: config.SecurityConfig{MaxRequestBytes: 1 << 20}}
	server, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	// Register a user so the tenant exists and an admin session is available.
	register := httptest.NewRecorder()
	registerRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBufferString(`{"email":"`+email+`","password":"supersecret"}`))
	registerRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(register, registerRequest)
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	token := register.Result().Cookies()[0].Value
	var registerBody struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Tenants []struct {
			ID string `json:"id"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(mustData(t, register.Body.Bytes()), &registerBody); err != nil {
		t.Fatal(err)
	}
	userID := registerBody.User.ID
	if userID == "" || len(registerBody.Tenants) == 0 {
		t.Fatalf("register response incomplete: %s", register.Body.String())
	}
	tenantID := registerBody.Tenants[0].ID
	// The session must be bound to the tenant before it can act as an admin.
	consolePost(t, handler, "/api/v1/auth/tenant-context", token, `{"tenant_id":"`+tenantID+`"}`)

	// An admin registers an agent bound to their own user.
	registration := consolePost(t, handler, "/api/v1/agents/register", token, consoleEnvelope(tenantID, userID, "agent-register-1", map[string]any{
		"name":             "Pi Agent",
		"allowed_user_ids": []string{userID},
		"allowed_scopes":   []string{"user-global", "session"},
		"capabilities":     []string{"observe", "project", "feedback"},
	}))
	var registrationBody struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(registration.Data, &registrationBody); err != nil {
		t.Fatal(err)
	}
	if registrationBody.ID == "" || registrationBody.Secret == "" {
		t.Fatalf("agent registration did not return id/secret: %s", string(registration.Data))
	}

	// The agent secret authenticates the bound user with no cookie.
	membersEnvelope := agentEnvelope(tenantID, userID, registrationBody.ID, "agent-members-1", map[string]any{})
	members := bearerPost(t, handler, "/api/v1/members", registrationBody.Secret, membersEnvelope)
	if members.Error != nil {
		t.Fatalf("agent-authenticated members call failed: %s", members.Error.Message)
	}

	// A credential alone is enough: the server resolves the bound user and tenant
	// when the envelope leaves them empty.
	minimal := agentEnvelope("", "", "mengpo-pi-10.0.0.1", "agent-members-minimal", map[string]any{})
	minimalResult := bearerPost(t, handler, "/api/v1/members", registrationBody.Secret, minimal)
	if minimalResult.Error != nil {
		t.Fatalf("minimal agent envelope failed: %s", minimalResult.Error.Message)
	}

	// A credential may not act for a user outside its allowed set.
	otherEnvelope := agentEnvelope(tenantID, "00000000-0000-4000-8000-000000000000", registrationBody.ID, "agent-members-2", map[string]any{})
	forbidden := bearerPost(t, handler, "/api/v1/members", registrationBody.Secret, otherEnvelope)
	if forbidden.Error == nil {
		t.Fatal("agent credential must not act for an unbound user")
	}
}

// agentEnvelope builds a v1 envelope with an agent principal.
func agentEnvelope(tenantID, userID, agentID, requestID string, payload any) string {
	encoded, _ := json.Marshal(payload)
	body, _ := json.Marshal(map[string]any{
		"version":         "v1",
		"request_id":      requestID,
		"idempotency_key": requestID,
		"principal":       map[string]any{"type": "agent", "id": agentID},
		"scope":           map[string]any{"tenant_id": tenantID, "user_id": userID, "type": "user-global"},
		"privacy":         map[string]any{"visibility": "private"},
		"payload":         json.RawMessage(encoded),
	})
	return string(body)
}

func bearerPost(t *testing.T, handler http.Handler, path, secret, body string) apiResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response apiResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s: %v body=%s", path, err, recorder.Body.String())
	}
	return response
}

func mustData(t *testing.T, body []byte) []byte {
	t.Helper()
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}
