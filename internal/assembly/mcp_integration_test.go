package assembly

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hchw/mengpo/internal/config"
)

type bearerTransport struct {
	secret string
	base   http.RoundTripper
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.secret)
	return t.base.RoundTrip(cloned)
}

func mcpConnect(t *testing.T, endpoint, secret string) (*sdkmcp.ClientSession, error) {
	t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "integration-client", Version: "1.0.0"}, nil)
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerTransport{secret: secret, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}
	return client.Connect(context.Background(), transport, nil)
}

// TestMCPServerOverStreamableHTTP drives the MCP endpoint through the real
// server, an agent credential registered by an admin, and the full command
// surface: tool listing, session binding, remember, and recall.
func TestMCPServerOverStreamableHTTP(t *testing.T) {
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
	const email = "mcp-owner@example.com"
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
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	// The MCP mount must not disturb the health and metrics surfaces.
	for _, path := range []string{"/healthz", "/metrics"} {
		response, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.StatusCode)
		}
	}

	// Register a user, bind the console session to its tenant, and register an
	// agent holding every capability.
	register := httptest.NewRecorder()
	registerRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"`+email+`","password":"supersecret"}`))
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
	tenantID := registerBody.Tenants[0].ID
	consolePost(t, handler, "/api/v1/auth/tenant-context", token, `{"tenant_id":"`+tenantID+`"}`)

	registration := consolePost(t, handler, "/api/v1/agents/register", token, consoleEnvelope(tenantID, userID, "mcp-agent-register", map[string]any{
		"name":             "MCP Agent",
		"allowed_user_ids": []string{userID},
		"allowed_scopes":   []string{"user-global", "session"},
		"capabilities":     []string{"observe", "project", "feedback", "session", "consolidate"},
	}))
	var registrationBody struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(registration.Data, &registrationBody); err != nil {
		t.Fatal(err)
	}
	if registrationBody.Secret == "" {
		t.Fatalf("agent registration failed: %s", string(registration.Data))
	}

	// An invalid credential must not connect.
	if _, err := mcpConnect(t, httpServer.URL+"/mcp", "not-a-secret"); err == nil {
		t.Fatal("invalid credential must not establish an MCP session")
	}

	session, err := mcpConnect(t, httpServer.URL+"/mcp", registrationBody.Secret)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"memory_recall", "memory_remember", "memory_bind_session", "memory_consolidate"} {
		if !names[want] {
			t.Fatalf("missing tool %q in %v", want, names)
		}
	}

	bound, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_bind_session", Arguments: map[string]any{"external_id": "it-session", "title": "integration"}})
	if err != nil || bound.IsError {
		t.Fatalf("bind_session=%#v err=%v", bound, err)
	}
	structured, _ := bound.StructuredContent.(map[string]any)
	sessionID, _ := structured["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("bind_session returned no session id: %#v", bound.StructuredContent)
	}

	remembered, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_remember", Arguments: map[string]any{"text": "the MCP endpoint shares the agent credential", "session_id": sessionID}})
	if err != nil || remembered.IsError {
		t.Fatalf("remember=%#v err=%v", remembered, err)
	}

	recalled, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_recall", Arguments: map[string]any{"query": "MCP endpoint credential", "session_id": sessionID}})
	if err != nil || recalled.IsError {
		t.Fatalf("recall=%#v err=%v", recalled, err)
	}

	// A bound agent may not act for a user outside its allow-list; the tool call
	// must surface a permission failure rather than widen scope.
	foreign, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_remember", Arguments: map[string]any{"text": "x", "user_id": "00000000-0000-4000-8000-000000000000"}})
	if err != nil {
		t.Fatalf("foreign user call transport error: %v", err)
	}
	if !foreign.IsError {
		t.Fatal("agent must not write for an unbound user")
	}
}
