package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/maintenance"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type alwaysSameFact struct{}

func (alwaysSameFact) ConfirmSameFact(context.Context, ports.AnalysisBatch) (bool, error) {
	return true, nil
}

type recordingRuns struct{ finished []ports.AnalysisRunUpdate }

func (r *recordingRuns) StartAnalysisRun(context.Context, string, ports.AnalysisRunRecord) (bool, error) {
	return true, nil
}
func (r *recordingRuns) FinishAnalysisRun(_ context.Context, _ string, _ string, update ports.AnalysisRunUpdate) (bool, error) {
	r.finished = append(r.finished, update)
	return true, nil
}
func (r *recordingRuns) ListAnalysisRuns(context.Context, string, ports.AnalysisRunFilter) ([]ports.AnalysisRunView, error) {
	return nil, nil
}
func (r *recordingRuns) LinkRunOutputs(context.Context, string, string, []string) (bool, error) {
	return true, nil
}

func insertSessionRow(t *testing.T, ctx context.Context, db *sql.DB, schema, sessionID, userID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.sessions (id, user_id, status) VALUES ($1,$2,'active')`, schema), sessionID, userID); err != nil {
		t.Fatalf("insert session %s: %v", sessionID, err)
	}
}

func insertCandidateWithEmbedding(t *testing.T, ctx context.Context, db *sql.DB, schema, id, userID, contentText string, axis int) {
	t.Helper()
	vector := fmt.Sprintf("'%s'::vector", vectorAxis(axis))
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.memory_nodes
		(id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, confidence,
		 applicability, content, content_text, default_retrieval, provenance,
		 embedding, embedding_model, embedding_artifact, embedding_version, embedding_status)
		VALUES ($1, $2, $3, 'user-global', $3, 'fact', 'candidate', 0.6,
		 '{}'::jsonb, '{}'::jsonb, $4, false, '{}'::jsonb,
		 %s, 'bge-test', 'artifact', 'v1', 'ready')`, schema, vector),
		id, "idem-"+id, userID, contentText); err != nil {
		t.Fatalf("insert candidate %s: %v", id, err)
	}
}

func insertSessionEvent(t *testing.T, ctx context.Context, db *sql.DB, schema, eventID, sessionID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.observed_events
		(id, idempotency_key, session_id, source_type, source_id, message_type, occurred_at, retention_class, reliability)
		VALUES ($1, $2, $3, 'user', 'user-1', 'message', now(), 'standard', 'high')`, schema),
		eventID, "idem-"+eventID, sessionID); err != nil {
		t.Fatalf("insert observed event %s: %v", eventID, err)
	}
}

func insertNodeEvidence(t *testing.T, ctx context.Context, db *sql.DB, schema, memoryID, eventID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.memory_evidence
		(id, memory_id, raw_event_id, evidence_role, confidence, attribution)
		VALUES (gen_random_uuid(), $1, $2, 'supports', 0.7, 'direct')`, schema), memoryID, eventID); err != nil {
		t.Fatalf("insert evidence %s->%s: %v", memoryID, eventID, err)
	}
}

// TestNearDuplicateJobEndToEnd drives the real maintenance job against the real
// repositories: two user-global candidates with identical embeddings and
// evidence from two different sessions must be merged, and the target must end
// up with both evidence rows (so governance sees two independent sessions).
func TestNearDuplicateJobEndToEnd(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "near duplicate e2e")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	const (
		userID   = "00000000-0000-4000-8000-000000001001"
		session1 = "00000000-0000-4000-8000-000000001002"
		session2 = "00000000-0000-4000-8000-000000001003"
		targetID = "00000000-0000-4000-8000-000000001101"
		dupID    = "00000000-0000-4000-8000-000000001102"
		targetEv = "00000000-0000-4000-8000-000000001201"
		dupEv    = "00000000-0000-4000-8000-000000001202"
	)
	insertSessionRow(t, ctx, db, tenant.Schema, session1, userID)
	insertSessionRow(t, ctx, db, tenant.Schema, session2, userID)
	insertCandidateWithEmbedding(t, ctx, db, tenant.Schema, targetID, userID, "user prefers concise answers", 0)
	insertCandidateWithEmbedding(t, ctx, db, tenant.Schema, dupID, userID, "user likes terse replies", 0)
	insertSessionEvent(t, ctx, db, tenant.Schema, targetEv, session1)
	insertSessionEvent(t, ctx, db, tenant.Schema, dupEv, session2)
	insertNodeEvidence(t, ctx, db, tenant.Schema, targetID, targetEv)
	insertNodeEvidence(t, ctx, db, tenant.Schema, dupID, dupEv)

	memoryRepository := NewMemoryRepository(router)
	candidateRepository := NewCandidateRepository(router)
	triageRepository := NewTriageRepository(router)
	runs := &recordingRuns{}
	job := &maintenance.NearDuplicateJob{
		PlanName:        "near-duplicate",
		Reader:          memoryRepository,
		Neighbors:       NewSemanticNeighborRepository(router),
		Triage:          triageRepository,
		Evidence:        candidateRepository,
		Merge:           memoryRepository,
		Confirmer:       alwaysSameFact{},
		Runs:            runs,
		PromptVersion:   "merge-v1",
		SchemaVersion:   "schema-v1",
		Threshold:       0.9,
		MergeEnabled:    true,
		LexicalFallback: false,
	}
	if err := job.Run(ctx, tenant.ID); err != nil {
		t.Fatalf("NearDuplicateJob.Run() error = %v", err)
	}
	if len(runs.finished) != 1 {
		t.Fatalf("runs finished = %d, want 1", len(runs.finished))
	}

	// One of the two survives as the active target; the other is retired.
	var survivorID, retiredID string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT id::text FROM %s.memory_nodes WHERE status='active' AND user_id=$1::uuid`, tenant.Schema), userID).Scan(&survivorID); err != nil {
		t.Fatalf("no active survivor: %v", err)
	}
	retiredID = dupID
	if survivorID == dupID {
		retiredID = targetID
	}
	var retiredStatus string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT status FROM %s.memory_nodes WHERE id=$1::uuid`, tenant.Schema), retiredID).Scan(&retiredStatus); err != nil {
		t.Fatal(err)
	}
	if retiredStatus != "rejected" {
		t.Fatalf("retired node status = %q, want rejected", retiredStatus)
	}

	// The survivor's evidence now spans both sessions, which is what makes the
	// governance recurrence channel reachable.
	var evidenceCount, sessionCount int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT count(*), count(DISTINCT o.session_id)
		FROM %s.memory_evidence e JOIN %s.observed_events o ON o.id = e.raw_event_id
		WHERE e.memory_id = $1::uuid`, tenant.Schema, tenant.Schema), survivorID).Scan(&evidenceCount, &sessionCount); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 2 || sessionCount != 2 {
		t.Fatalf("survivor evidence = (%d rows, %d sessions), want (2,2)", evidenceCount, sessionCount)
	}
}
