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

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func consoleEnvelope(tenantID, userID, requestID string, payload any) string {
	encoded, _ := json.Marshal(payload)
	body, _ := json.Marshal(map[string]any{
		"version":         "v1",
		"request_id":      requestID,
		"idempotency_key": requestID,
		"principal":       map[string]any{"type": "user", "id": userID},
		"scope":           map[string]any{"tenant_id": tenantID, "user_id": userID, "type": "user-global"},
		"privacy":         map[string]any{"visibility": "private"},
		"payload":         json.RawMessage(encoded),
	})
	return string(body)
}

func consolePost(t *testing.T, handler http.Handler, path, token, body string) apiResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "memory_session", Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	var response apiResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if response.Error != nil {
		t.Fatalf("POST %s error %s", path, response.Error.Message)
	}
	return response
}

// TestConsoleEndpoints exercises the console read/admin surface end to end.
func TestConsoleEndpoints(t *testing.T) {
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
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "console endpoints")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.users WHERE email='console@example.com'`)
	})

	cfg := config.Config{Environment: "development", Auth: config.AuthConfig{SessionCookieName: "memory_session", SessionTTL: time.Hour}, Security: config.SecurityConfig{MaxRequestBytes: 1 << 20}}
	server, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	exchange := httptest.NewRecorder()
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sso/exchange", bytes.NewBufferString(`{"assertion":"console@example.com"}`))
	exchangeRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(exchange, exchangeRequest)
	if exchange.Code != http.StatusOK {
		t.Fatalf("exchange status=%d", exchange.Code)
	}
	token := exchange.Result().Cookies()[0].Value
	var exchangeBody struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	_ = json.Unmarshal(exchange.Body.Bytes(), &exchangeBody)
	userID := exchangeBody.Data.User.ID
	if _, err := db.ExecContext(ctx, `INSERT INTO public.tenant_memberships (id, user_id, tenant_id, status) VALUES ($1::uuid,$2::uuid,$3::uuid,'active')`, randomMemberID(t), userID, tenant.ID); err != nil {
		t.Fatal(err)
	}
	consolePost(t, handler, "/api/v1/auth/tenant-context", token, `{"tenant_id":"`+tenant.ID+`"}`)

	router := tenantdb.NewRouter(db, store)
	memories := postgres.NewMemoryRepository(router)
	activeID := "00000000-0000-4000-8000-0000000c0a01"
	candidateID := "00000000-0000-4000-8000-0000000c0a02"
	if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: activeID, IdempotencyKey: "console-active", UserID: userID, ScopeType: "user-global", ScopeID: userID,
		MemoryType: "fact", Status: "active", Confidence: 0.9, DefaultRetrieval: true,
		Content: json.RawMessage(`{"summary":"active memory"}`), ContentText: "active memory", Provenance: json.RawMessage(`{"evidence_event_ids":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: candidateID, IdempotencyKey: "console-candidate", UserID: userID, ScopeType: "user-global", ScopeID: userID,
		MemoryType: "fact", Status: "candidate", Confidence: 0.5, DefaultRetrieval: false,
		Content: json.RawMessage(`{"summary":"candidate memory"}`), ContentText: "candidate memory", Provenance: json.RawMessage(`{"evidence_event_ids":[]}`),
	}); err != nil {
		t.Fatal(err)
	}

	// Lists.
	if resp := consolePost(t, handler, "/api/v1/memories", token, consoleEnvelope(tenant.ID, userID, "list-memories", map[string]any{"page": 1, "page_size": 20})); !bytes.Contains(resp.Data, []byte(activeID)) {
		t.Fatalf("memories missing active: %s", resp.Data)
	}
	if resp := consolePost(t, handler, "/api/v1/candidates", token, consoleEnvelope(tenant.ID, userID, "list-candidates", map[string]any{"page": 1, "page_size": 20})); !bytes.Contains(resp.Data, []byte(candidateID)) {
		t.Fatalf("candidates missing candidate: %s", resp.Data)
	}

	// Governance actions.
	consolePost(t, handler, "/api/v1/candidates/"+candidateID+"/reject", token, consoleEnvelope(tenant.ID, userID, "reject-candidate", map[string]any{}))
	if node, err := memories.Get(ctx, tenant.ID, candidateID); err != nil || node.Status != "rejected" {
		t.Fatalf("reject status=%s err=%v", node.Status, err)
	}

	// correct creates a replacement memory and supersedes relation.
	candidate2 := "00000000-0000-4000-8000-0000000c0a03"
	if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: candidate2, IdempotencyKey: "console-candidate-2", UserID: userID, ScopeType: "user-global", ScopeID: userID,
		MemoryType: "fact", Status: "candidate", Confidence: 0.5, DefaultRetrieval: false,
		Content: json.RawMessage(`{"summary":"needs correcting"}`), ContentText: "needs correcting", Provenance: json.RawMessage(`{"evidence_event_ids":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	consolePost(t, handler, "/api/v1/candidates/"+candidate2+"/correct", token, consoleEnvelope(tenant.ID, userID, "correct-candidate", map[string]any{"content_summary": "corrected summary"}))
	var replacements int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_relations WHERE relation_type='supersedes'`).Scan(&replacements)
	}); err != nil || replacements == 0 {
		t.Fatalf("correction relation count=%d err=%v", replacements, err)
	}

	// Single memory read.
	if resp := consolePost(t, handler, "/api/v1/memories/"+activeID, token, consoleEnvelope(tenant.ID, userID, "get-memory", map[string]any{})); !bytes.Contains(resp.Data, []byte(activeID)) {
		t.Fatalf("single memory missing: %s", resp.Data)
	}

	// merge unifies duplicates: evidence is unioned, the duplicate is retired
	// and linked with a merged_into relation, and confidence rises.
	dupTarget := "00000000-0000-4000-8000-0000000c0a04"
	dupOther := "00000000-0000-4000-8000-0000000c0a05"
	for _, node := range []struct {
		id, key, evidence string
	}{{dupTarget, "merge-target", "ev-1"}, {dupOther, "merge-other", "ev-2"}} {
		if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
			ID: node.id, IdempotencyKey: node.key, UserID: userID, ScopeType: "user-global", ScopeID: userID,
			MemoryType: "fact", Status: "candidate", Confidence: 0.5, DefaultRetrieval: false,
			Content: json.RawMessage(`{"summary":"duplicate"}`), ContentText: "duplicate",
			Provenance: json.RawMessage(`{"evidence_event_ids":["` + node.evidence + `"]}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	consolePost(t, handler, "/api/v1/candidates/"+dupTarget+"/merge", token, consoleEnvelope(tenant.ID, userID, "merge-candidates", map[string]any{"duplicate_memory_ids": []string{dupOther}}))
	merged, err := memories.Get(ctx, tenant.ID, dupTarget)
	if err != nil || merged.Confidence <= 0.5 || merged.Status != "active" {
		t.Fatalf("merge target confidence=%.2f status=%s err=%v", merged.Confidence, merged.Status, err)
	}
	var mergedProvenance struct {
		EvidenceEventIDs []string `json:"evidence_event_ids"`
	}
	if err := json.Unmarshal(merged.Provenance, &mergedProvenance); err != nil || len(mergedProvenance.EvidenceEventIDs) != 2 {
		t.Fatalf("merged evidence=%s err=%v", merged.Provenance, err)
	}
	retired, err := memories.Get(ctx, tenant.ID, dupOther)
	if err != nil || retired.Status != "rejected" {
		t.Fatalf("merged duplicate status=%s err=%v", retired.Status, err)
	}
	var mergedRelations int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_relations WHERE relation_type='merged_into' AND source_memory_id=$1::uuid AND target_memory_id=$2::uuid`, dupOther, dupTarget).Scan(&mergedRelations)
	}); err != nil || mergedRelations != 1 {
		t.Fatalf("merged_into relations=%d err=%v", mergedRelations, err)
	}

	// Degraded/empty projections.
	consolePost(t, handler, "/api/v1/failures", token, consoleEnvelope(tenant.ID, userID, "failures", map[string]any{}))
	consolePost(t, handler, "/api/v1/evaluation", token, consoleEnvelope(tenant.ID, userID, "evaluation", map[string]any{}))

	// Members and agents.
	if resp := consolePost(t, handler, "/api/v1/members", token, consoleEnvelope(tenant.ID, userID, "members", map[string]any{})); !bytes.Contains(resp.Data, []byte(userID)) {
		t.Fatalf("members missing user: %s", resp.Data)
	}
	agentRepo := postgres.NewAgentRepository(db)
	agentID := "00000000-0000-4000-8000-0000000c0b01"
	if err := agentRepo.CreateAgent(ctx, auth.Agent{ID: agentID, TenantID: tenant.ID, Name: "console-agent", AllowedScopes: []string{"session"}, Capabilities: []string{"observe"}, Status: auth.AgentActive}, auth.AgentCredential{ID: "cred_" + agentID, AgentID: agentID, TenantID: tenant.ID, SecretHash: []byte("hash")}); err != nil {
		t.Fatal(err)
	}
	if resp := consolePost(t, handler, "/api/v1/agents", token, consoleEnvelope(tenant.ID, userID, "agents", map[string]any{})); !bytes.Contains(resp.Data, []byte(agentID)) {
		t.Fatalf("agents missing agent: %s", resp.Data)
	}
	consolePost(t, handler, "/api/v1/agents/"+agentID+"/disable", token, consoleEnvelope(tenant.ID, userID, "disable-agent", map[string]any{}))
	if listed, err := agentRepo.ListAgents(ctx, tenant.ID); err != nil || listed[0].Status != auth.AgentDisabled {
		t.Fatalf("agent status=%s err=%v", listed[0].Status, err)
	}
}
