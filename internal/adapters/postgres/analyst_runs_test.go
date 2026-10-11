package postgres

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAnalysisRunStoreRecordsIdempotentRuns(t *testing.T) {
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
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "analysis runs")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repository := NewAnalystRunRepository(router)

	jobID := "00000000-0000-4000-8000-0000000000ee"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO outbox_jobs (id, job_type, tenant_id, idempotency_key, status)
VALUES ($1, 'normalize_event', $2, 'run-record-1', 'queued')`, jobID, tenant.ID)
		return err
	}); err != nil {
		t.Fatalf("seed outbox job: %v", err)
	}

	record := ports.AnalysisRunRecord{
		ID:            "00000000-0000-4000-8000-0000000000ff",
		RunID:         jobID,
		TaskType:      "analyze_failure",
		Trigger:       ports.TriggerEvent,
		Provider:      "openai-compatible",
		Model:         "test-model",
		PromptVersion: "analyze_failure-llm-v1",
		SchemaVersion: "schema-v1",
		InputEventIDs: []string{"00000000-0000-4000-8000-0000000000aa"},
		Status:        ports.AnalysisRunRunning,
	}
	inserted, err := repository.StartAnalysisRun(ctx, tenant.ID, record)
	if err != nil {
		t.Fatalf("StartAnalysisRun() error = %v", err)
	}
	if !inserted {
		t.Fatal("first StartAnalysisRun did not insert")
	}
	// Retrying the same correlation id must not create a second record.
	replay := record
	replay.ID = "00000000-0000-4000-8000-000000000100"
	inserted, err = repository.StartAnalysisRun(ctx, tenant.ID, replay)
	if err != nil {
		t.Fatalf("replay StartAnalysisRun() error = %v", err)
	}
	if inserted {
		t.Fatal("replay StartAnalysisRun inserted a duplicate record")
	}

	// A run that is still in flight has a NULL result column; listing must
	// tolerate it rather than failing the whole page with a scan error.
	inFlight, err := repository.ListAnalysisRuns(ctx, tenant.ID, ports.AnalysisRunFilter{})
	if err != nil {
		t.Fatalf("ListAnalysisRuns(in-flight) error = %v", err)
	}
	if len(inFlight) != 1 || string(inFlight[0].Result) != "null" {
		t.Fatalf("in-flight run result = %q, want json null", inFlight[0].Result)
	}

	updated, err := repository.FinishAnalysisRun(ctx, tenant.ID, jobID, ports.AnalysisRunUpdate{
		Status:           ports.AnalysisRunSucceeded,
		LatencyMS:        42,
		TokensPrompt:     120,
		TokensCompletion: 30,
		CandidateCount:   2,
		DiscardedCount:   1,
		ConflictCount:    0,
	})
	if err != nil || !updated {
		t.Fatalf("FinishAnalysisRun() updated=%v err=%v", updated, err)
	}

	runs, err := repository.ListAnalysisRuns(ctx, tenant.ID, ports.AnalysisRunFilter{TaskType: "analyze_failure"})
	if err != nil {
		t.Fatalf("ListAnalysisRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	got := runs[0]
	if got.Provider != "openai-compatible" || got.Model != "test-model" || got.PromptVersion != "analyze_failure-llm-v1" {
		t.Fatalf("run provenance = %+v", got)
	}
	if got.TokensPrompt != 120 || got.TokensCompletion != 30 || got.LatencyMS != 42 {
		t.Fatalf("run metering = %+v", got)
	}
	if got.CandidateCount != 2 || got.DiscardedCount != 1 || got.Status != ports.AnalysisRunSucceeded {
		t.Fatalf("run summary = %+v", got)
	}
	if len(got.InputEventIDs) != 1 {
		t.Fatalf("run input refs = %#v", got.InputEventIDs)
	}

	// A degraded run records its reason and is queryable by status.
	degradedJob := "00000000-0000-4000-8000-000000000102"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO outbox_jobs (id, job_type, tenant_id, idempotency_key, status)
VALUES ($1, 'normalize_event', $2, 'run-record-2', 'queued')`, degradedJob, tenant.ID)
		return err
	}); err != nil {
		t.Fatalf("seed degraded outbox job: %v", err)
	}
	if _, err := repository.StartAnalysisRun(ctx, tenant.ID, ports.AnalysisRunRecord{
		ID: "00000000-0000-4000-8000-000000000103", RunID: degradedJob, TaskType: "consolidate_memory",
	}); err != nil {
		t.Fatalf("StartAnalysisRun(degraded) error = %v", err)
	}
	if _, err := repository.FinishAnalysisRun(ctx, tenant.ID, degradedJob, ports.AnalysisRunUpdate{
		Status:         ports.AnalysisRunDeadLetter,
		LastError:      "provider unavailable",
		DegradedReason: "rules_only",
		DiscardedCount: 3,
	}); err != nil {
		t.Fatalf("FinishAnalysisRun(degraded) error = %v", err)
	}
	degraded, err := repository.ListAnalysisRuns(ctx, tenant.ID, ports.AnalysisRunFilter{Status: ports.AnalysisRunDeadLetter})
	if err != nil {
		t.Fatalf("ListAnalysisRuns(failed) error = %v", err)
	}
	if len(degraded) != 1 || degraded[0].DegradedReason != "rules_only" || degraded[0].DiscardedCount != 3 {
		t.Fatalf("degraded run = %#v", degraded)
	}
}

func TestMaintenanceCursorRoundTrip(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "maintenance cursor")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	repository := NewAnalystRunRepository(tenantdb.NewRouter(db, store))

	if watermark, err := repository.LoadMaintenanceCursor(ctx, tenant.ID, "consolidate"); err != nil || watermark != 0 {
		t.Fatalf("initial cursor = %d err=%v", watermark, err)
	}
	if err := repository.SaveMaintenanceCursor(ctx, tenant.ID, "consolidate", 5); err != nil {
		t.Fatalf("SaveMaintenanceCursor() error = %v", err)
	}
	// The cursor only moves forward.
	if err := repository.SaveMaintenanceCursor(ctx, tenant.ID, "consolidate", 3); err != nil {
		t.Fatalf("SaveMaintenanceCursor(rewind) error = %v", err)
	}
	watermark, err := repository.LoadMaintenanceCursor(ctx, tenant.ID, "consolidate")
	if err != nil || watermark != 5 {
		t.Fatalf("cursor = %d err=%v, want 5", watermark, err)
	}
}

func TestAnalysisRunDetailIncludesInputsAndOutputs(t *testing.T) {
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
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "analysis run detail")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repository := NewAnalystRunRepository(router)

	const userID = "00000000-0000-4000-8000-0000000000a1"
	const sessionID = "00000000-0000-4000-8000-0000000000a2"
	const eventID = "00000000-0000-4000-8000-0000000000a3"
	const memoryID = "00000000-0000-4000-8000-0000000000a4"
	const runID = "00000000-0000-4000-8000-0000000000a5"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status, title) VALUES ($1::uuid, $2::uuid, 'active', 'detail')`, sessionID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO observed_events (id, idempotency_key, session_id, source_type, source_id, message_type, payload, occurred_at, retention_class)
			VALUES ($1::uuid, 'detail-event', $2::uuid, 'tool', 'src', 'tool.failure', '{"stderr":"boom"}'::jsonb, now(), 'standard')`, eventID, sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_nodes (id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, visibility, confidence, content, content_text, default_retrieval, version, provenance)
			VALUES ($1::uuid, 'detail-memory', $2::uuid, 'user-global', $2::uuid, 'insight', 'candidate', 'private', 0.6, '{"summary":"boom on retry"}'::jsonb, '', false, 1, '{}'::jsonb)`, memoryID, userID)
		return err
	}); err != nil {
		t.Fatalf("seed detail fixtures: %v", err)
	}

	record := ports.AnalysisRunRecord{
		ID: "00000000-0000-4000-8000-0000000000a6", RunID: runID, TaskType: "analyze_failure",
		Trigger: ports.TriggerEvent, InputEventIDs: []string{eventID}, Status: ports.AnalysisRunRunning,
	}
	if _, err := repository.StartAnalysisRun(ctx, tenant.ID, record); err != nil {
		t.Fatalf("StartAnalysisRun() error = %v", err)
	}
	if _, err := repository.FinishAnalysisRun(ctx, tenant.ID, runID, ports.AnalysisRunUpdate{
		Status: ports.AnalysisRunSucceeded, CandidateCount: 1, OutputMemoryIDs: []string{memoryID},
	}); err != nil {
		t.Fatalf("FinishAnalysisRun() error = %v", err)
	}

	detail, found, err := repository.GetAnalysisRunDetail(ctx, tenant.ID, record.ID, 50)
	if err != nil || !found {
		t.Fatalf("GetAnalysisRunDetail() found=%v err=%v", found, err)
	}
	if detail.Run.TaskType != "analyze_failure" || len(detail.Run.OutputMemoryIDs) != 1 {
		t.Fatalf("detail run = %#v", detail.Run)
	}
	if len(detail.Inputs) != 1 || detail.Inputs[0].MessageType != "tool.failure" || detail.Inputs[0].Payload == "" {
		t.Fatalf("detail inputs = %#v", detail.Inputs)
	}
	if len(detail.Outputs) != 1 || detail.Outputs[0].ID != memoryID || detail.Outputs[0].Status != "candidate" || !strings.Contains(detail.Outputs[0].ContentText, "boom on retry") {
		t.Fatalf("detail outputs = %#v", detail.Outputs)
	}
	if detail.Truncated {
		t.Fatal("detail unexpectedly reported truncation")
	}

	if _, found, err := repository.GetAnalysisRunDetail(ctx, tenant.ID, "00000000-0000-4000-8000-0000000000ff", 50); err != nil || found {
		t.Fatalf("unknown run found=%v err=%v", found, err)
	}
}
