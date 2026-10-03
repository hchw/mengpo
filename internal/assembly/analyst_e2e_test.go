package assembly

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/adapters/llm"
	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/governance"
	appobservation "github.com/hchw/mengpo/internal/application/observation"
	"github.com/hchw/mengpo/internal/domain/memory"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestAnalystEndToEnd drives the whole LLM curation path against real
// PostgreSQL and a local fake LLM HTTP server: observe -> normalize -> model
// candidates -> governance.
func TestAnalystEndToEnd(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "analyst end to end")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	userID := "00000000-0000-4000-8000-000000000201"
	sessionID := "00000000-0000-4000-8000-000000000202"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status, title) VALUES ($1, $2, 'active', 'e2e')`, sessionID, userID)
		return err
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A fake OpenAI-compatible provider that returns one candidate grounded in
	// the observed event.
	var serverEvent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"candidates":[{"candidate_id":"e2e-1","evidence_event_ids":["` + serverEvent + `"],"scope_type":"user-global","scope_id":"` + userID + `","content":{"text":"the migration command fails on retry"},"confidence":0.7}]}`,
		}}}, "usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 12}})
		_, _ = w.Write(body)
	}))
	defer server.Close()

	gateway := appobservation.NewGateway(postgres.NewObservationRepository(router))
	event, created, err := gateway.IngestTool(ctx, appobservation.Principal{
		TenantID: tenant.ID, SourceID: userID, SourceType: obsdomain.SourceTool,
		AccessLevel: obsdomain.Level0, DeploymentBound: true,
	}, appobservation.Input{
		SessionID: sessionID, IdempotencyKey: "e2e-event-1", OccurredAt: time.Now().UTC(),
		Payload: json.RawMessage(`{"status":"error","text":"migration failed"}`), RetentionClass: "standard",
		Visibility: obsdomain.VisibilitySession, Reliability: obsdomain.ReliabilityHigh,
	})
	if err != nil || !created {
		t.Fatalf("ingest: created=%v err=%v", created, err)
	}
	serverEvent = event.ID

	adapter, err := llm.New(llm.Options{BaseURL: server.URL, Model: "e2e-model", AllowExternal: true, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("llm adapter: %v", err)
	}
	dispatcher := &JobDispatcher{
		Observations:         postgres.NewObservationRepository(router),
		Normalized:           postgres.NewNormalizedEventRepository(router),
		AnalysisReader:       postgres.NewNormalizedEventRepository(router),
		Candidates:           postgres.NewCandidateRepository(router),
		WorkingMemory:        postgres.NewWorkingMemoryRepository(router),
		SessionOwner:         postgres.NewSessionRepository(router),
		Runs:                 postgres.NewAnalystRunRepository(router),
		Analysis:             analysis.NewWithPrivacy(analysis.Providers{Analyst: analysis.ResilientService{Primary: adapter, Fallback: analysis.RuleFallback{}}}, analysis.PrivacyPolicy{}),
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		PromptVersion:        llm.PromptVersion,
		Provider:             "memory-llm",
		Model:                "e2e-model",
		NewID:                newUUID,
	}

	outbox := postgres.NewOutboxRepository(router)
	jobs, err := outbox.LeaseJobs(ctx, tenant.ID, "e2e-worker", 5, time.Minute)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("leased jobs=%#v err=%v", jobs, err)
	}
	if err := dispatcher.Handle(ctx, jobs[0]); err != nil {
		t.Fatalf("dispatch normalize_event: %v", err)
	}

	memoryRepository := postgres.NewMemoryRepository(router)
	page, err := memoryRepository.ListMemories(ctx, tenant.ID, ports.MemoryListRequest{UserID: userID, Statuses: []string{"candidate"}, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	var candidate ports.MemoryNodeRecord
	for _, node := range page.Items {
		if node.Status == "candidate" {
			candidate = node
		}
	}
	if candidate.ID == "" {
		t.Fatalf("no candidate produced from the model output: %#v", page.Items)
	}
	if candidate.DefaultRetrieval {
		t.Fatal("model candidate must not be default-retrievable")
	}

	// The run record captured the model and metering.
	runs, err := postgres.NewAnalystRunRepository(router).ListAnalysisRuns(ctx, tenant.ID, ports.AnalysisRunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("analysis runs = %#v err=%v", runs, err)
	}
	if runs[0].Provider != "memory-llm" || runs[0].Model != "e2e-model" || runs[0].TokensPrompt != 40 || runs[0].CandidateCount != 1 {
		t.Fatalf("run record = %+v", runs[0])
	}

	// Governance owns promotion: a reject moves the candidate out of review.
	governanceService := governance.NewService(memoryRepository)
	if _, err := governanceService.Apply(ctx, tenant.ID, candidate.ID, memory.GovernanceCommand{
		Action: memory.GovernanceReject, ExpectedVersion: candidate.Version,
		AuditID: "00000000-0000-4000-8000-000000000203", ActorType: "user", ActorID: userID,
		RequestID: "e2e-governance-1", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("governance reject: %v", err)
	}
	updated, err := memoryRepository.Get(ctx, tenant.ID, candidate.ID)
	if err != nil || updated.Status != "rejected" {
		t.Fatalf("candidate after governance = %+v err=%v", updated, err)
	}
}

// TestRunRecordsAreCleanedWhenAMemoryIsDeleted proves the produced-memory link
// is written and that a governance delete cancels the related run records.
func TestRunRecordsAreCleanedWhenAMemoryIsDeleted(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "run cleanup")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	userID := "00000000-0000-4000-8000-000000000301"
	memoryID := "00000000-0000-4000-8000-000000000302"

	candidates := postgres.NewCandidateRepository(router)
	if _, err := candidates.PersistCandidates(ctx, tenant.ID, []ports.CandidateMemoryRecord{{Node: ports.MemoryNodeRecord{
		ID: memoryID, IdempotencyKey: "cleanup-1", UserID: userID, ScopeType: "user-global", ScopeID: userID,
		MemoryType: "insight", Status: "candidate", Confidence: 0.5, Content: json.RawMessage(`{"text":"x"}`),
	}}}); err != nil {
		t.Fatalf("persist candidate: %v", err)
	}

	runs := postgres.NewAnalystRunRepository(router)
	if _, err := runs.StartAnalysisRun(ctx, tenant.ID, ports.AnalysisRunRecord{ID: "00000000-0000-4000-8000-000000000303", RunID: "corr-cleanup", TaskType: "consolidate_memory"}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := runs.LinkRunOutputs(ctx, tenant.ID, "corr-cleanup", []string{memoryID}); err != nil {
		t.Fatalf("link outputs: %v", err)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var linked []byte
		if err := tx.QueryRowContext(ctx, `SELECT produced_memory_ids FROM analysis_jobs WHERE correlation_id='corr-cleanup'`).Scan(&linked); err != nil {
			return err
		}
		if !strings.Contains(string(linked), memoryID) {
			return fmt.Errorf("produced_memory_ids not linked: %s", linked)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	memoryRepository := postgres.NewMemoryRepository(router)
	if _, err := governance.NewService(memoryRepository).Apply(ctx, tenant.ID, memoryID, memory.GovernanceCommand{
		Action: memory.GovernanceDelete, ExpectedVersion: 1,
		AuditID: "00000000-0000-4000-8000-000000000304", ActorType: "user", ActorID: userID,
		RequestID: "cleanup-delete", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("governance delete: %v", err)
	}
	var finalStatus string
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT status FROM analysis_jobs WHERE correlation_id='corr-cleanup'`).Scan(&finalStatus)
	}); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if finalStatus != "cancelled" {
		t.Fatalf("run status after delete = %q, want cancelled", finalStatus)
	}
}
