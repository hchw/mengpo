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

	"github.com/hchw/mengpo/db/migrations"
	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	testUserID    = "00000000-0000-4000-8000-00000000a001"
	testSessionID = "00000000-0000-4000-8000-00000000a002"
	testMemoryID  = "00000000-0000-4000-8000-00000000a003"
)

type staticAuthenticator struct {
	tenantID string
}

func (a staticAuthenticator) Authenticate(*http.Request) (agentaccess.Identity, error) {
	return agentaccess.Identity{
		TenantID:     a.tenantID,
		UserID:       testUserID,
		SourceID:     "test-deployment",
		Capabilities: []string{"observe", "project", "feedback"},
		Source:       agentaccess.SourceDeployment,
		AccessLevel:  observation.Level0,
	}, nil
}

type apiResponse struct {
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func postJSON(t *testing.T, handler http.Handler, path, body string) apiResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	var response apiResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s response: %v", path, err)
	}
	if response.Error != nil {
		t.Fatalf("POST %s returned error %s: %s", path, response.Error.Code, response.Error.Message)
	}
	return response
}

func envelopeBody(t *testing.T, tenantID, requestID, idempotencyKey, scopeType, sessionID string, payload any) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"tenant_id": tenantID, "user_id": testUserID, "type": scopeType}
	if sessionID != "" {
		scope["session_id"] = sessionID
	}
	body, err := json.Marshal(map[string]any{
		"version":         "v1",
		"request_id":      requestID,
		"idempotency_key": idempotencyKey,
		"principal":       map[string]any{"type": "user", "id": testUserID},
		"scope":           scope,
		"privacy":         map[string]any{"visibility": "private"},
		"payload":         json.RawMessage(encoded),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func createMigratedTenant(t *testing.T, ctx context.Context, db *sql.DB, store *registry.Store, name string) auth.Tenant {
	t.Helper()
	tenant, err := store.RegisterTenant(ctx, name)
	if err != nil {
		t.Fatalf("register tenant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+tenant.Schema); err != nil {
		t.Fatalf("create schema %s: %v", tenant.Schema, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tenant migration: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path TO `+tenant.Schema+`, public`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set tenant search_path: %v", err)
	}
	if err := migrations.ApplyTenant(ctx, tx, migrations.TenantFS()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply tenant migration: %v", err)
	}
	var migrationVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM tenant_migrations`).Scan(&migrationVersion); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read tenant migration version: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.tenant_schema_registry SET migration_version = $1 WHERE tenant_id = $2`, migrationVersion, tenant.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("record tenant migration version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tenant migration: %v", err)
	}
	if err := store.ActivateTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("activate tenant: %v", err)
	}
	return tenant
}

// TestHTTPCommandEndpoints exercises the assembled handler end to end against
// PostgreSQL: session binding, observe, project, feedback and consolidate all
// return real business results through the /api/v1 surface.
func TestHTTPCommandEndpoints(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "http command endpoints")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	useCases, err := BuildUseCases(config.Config{Security: config.SecurityConfig{MaxRequestBytes: 1 << 20}}, db)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(config.Config{Security: config.SecurityConfig{MaxRequestBytes: 1 << 20}}, useCases, staticAuthenticator{tenantID: tenant.ID}, nil, nil, nil, func(context.Context) error { return nil }, nil, nil)

	// Session binding.
	sessionResponse := postJSON(t, handler, "/api/v1/sessions", envelopeBody(t, tenant.ID, "req-session", "idem-session", "session", testSessionID, map[string]any{"external_id": "conv-1", "title": "demo"}))
	var sessionResult struct {
		SessionID string `json:"session_id"`
		TenantID  string `json:"tenant_id"`
	}
	if err := json.Unmarshal(sessionResponse.Data, &sessionResult); err != nil {
		t.Fatal(err)
	}
	if sessionResult.SessionID == "" || sessionResult.TenantID != tenant.ID {
		t.Fatalf("unexpected session result: %s", sessionResponse.Data)
	}

	// Observe persists a raw event.
	observeResponse := postJSON(t, handler, "/api/v1/observe", envelopeBody(t, tenant.ID, "req-observe", "idem-observe", "session", testSessionID, map[string]any{
		"source_event_id": "src-1",
		"message_type":    "message",
		"text":            "migration failed because of a lock",
		"payload":         map[string]any{"text": "migration failed because of a lock"},
	}))
	var observeResult struct {
		EventID string `json:"event_id"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(observeResponse.Data, &observeResult); err != nil {
		t.Fatal(err)
	}
	if observeResult.EventID == "" || !observeResult.Created {
		t.Fatalf("unexpected observe result: %s", observeResponse.Data)
	}

	// Seed one active memory so projection and feedback have a real target.
	memories := postgres.NewMemoryRepository(router)
	if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: testMemoryID, IdempotencyKey: "http-memory-1", UserID: testUserID, SessionID: testSessionID,
		ScopeType: "session", ScopeID: testSessionID, MemoryType: "failure", Status: "active",
		Confidence: 0.8, DefaultRetrieval: true, Content: json.RawMessage(`{"summary":"migration lock failure"}`),
		ContentText: "migration lock failure", Provenance: json.RawMessage(`{"evidence_event_ids":[]}`),
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	// Project runs hybrid recall and records a projection event.
	postJSON(t, handler, "/api/v1/project", envelopeBody(t, tenant.ID, "req-project", "idem-project", "session", testSessionID, map[string]any{"query": "migration lock"}))

	// Feedback is stored against the seeded memory.
	postJSON(t, handler, "/api/v1/feedback", envelopeBody(t, tenant.ID, "req-feedback", "idem-feedback", "session", testSessionID, map[string]any{"memory_id": testMemoryID, "type": "helpful", "reason": "useful"}))

	// Consolidate enqueues a durable job.
	postJSON(t, handler, "/api/v1/consolidate", envelopeBody(t, tenant.ID, "req-consolidate", "idem-consolidate", "session", testSessionID, map[string]any{}))

	var feedbackCount, projectionCount, consolidateJobs int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_feedback WHERE memory_id=$1`, testMemoryID).Scan(&feedbackCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM projection_events`).Scan(&projectionCount); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox_jobs WHERE job_type=$1`, ConsolidateJobType).Scan(&consolidateJobs)
	}); err != nil {
		t.Fatal(err)
	}
	if feedbackCount != 1 || projectionCount != 1 || consolidateJobs != 1 {
		t.Fatalf("feedback=%d projection=%d consolidateJobs=%d", feedbackCount, projectionCount, consolidateJobs)
	}
}
