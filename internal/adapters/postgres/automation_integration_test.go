package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestEvidenceAndTriageAdapters exercises the read-side projections that
// promotion and the review surface depend on: evidence joined with its source
// event, influence detection, and triage artifacts.
func TestEvidenceAndTriageAdapters(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "evidence adapters")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repository := NewCandidateRepository(router)
	triage := NewTriageRepository(router)

	const userID = "00000000-0000-4000-8000-000000000001"
	const sessionID = "00000000-0000-4000-8000-0000000000a1"
	const memoryID = "00000000-0000-4000-8000-0000000000b1"
	const memoryID2 = "00000000-0000-4000-8000-0000000000b2"
	const eventID = "00000000-0000-4000-8000-0000000000c1"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status, title) VALUES ($1::uuid, $2::uuid, 'active', 'test')`, sessionID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO observed_events (id, idempotency_key, session_id, source_type, source_id, message_type, payload, occurred_at, reliability, retention_class, attribution_level, trace_metadata)
			VALUES ($1::uuid, 'evidence-event', $2::uuid, 'tool', 'src', 'tool.failure', '{}'::jsonb, now(), 'high', 'standard', 'direct', '{"used_memory_ids":["`+memoryID+`"]}'::jsonb)`, eventID, sessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_nodes (id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, visibility, confidence, content, content_text, default_retrieval, version, provenance)
			VALUES ($1::uuid, 'evidence-memory', $2::uuid, 'user-global', $2::uuid, 'insight', 'candidate', 'private', 0.6, '{"summary":"x"}'::jsonb, 'x', false, 1, '{}'::jsonb)`, memoryID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_nodes (id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, visibility, confidence, content, content_text, default_retrieval, version, provenance)
			VALUES ($1::uuid, 'evidence-memory-2', $2::uuid, 'user-global', $2::uuid, 'insight', 'candidate', 'private', 0.6, '{"summary":"y"}'::jsonb, 'y', false, 1, '{}'::jsonb)`, memoryID2, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_evidence (id, memory_id, raw_event_id, evidence_role, confidence, attribution)
			VALUES (gen_random_uuid(), $1::uuid, $2::uuid, 'supports', 0.6, 'direct')`, memoryID, eventID)
		return err
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}

	evidence, err := repository.ListMemoryEvidence(ctx, tenant.ID, memoryID)
	if err != nil {
		t.Fatalf("ListMemoryEvidence() error = %v", err)
	}
	if len(evidence) != 1 || evidence[0].Reliability != "high" || evidence[0].SourceSessionID != sessionID || evidence[0].Attribution != "direct" {
		t.Fatalf("evidence = %#v", evidence)
	}

	influenced, err := repository.InfluencedSessions(ctx, tenant.ID, memoryID, []string{sessionID})
	if err != nil {
		t.Fatalf("InfluencedSessions() error = %v", err)
	}
	if _, ok := influenced[sessionID]; !ok {
		t.Fatalf("influenced sessions = %#v, want %s", influenced, sessionID)
	}

	record := ports.CandidateTriageRecord{
		ID: "00000000-0000-4000-8000-0000000000d1", MemoryID: memoryID, RunID: "run-1",
		DuplicateGroup: "group-1", ConflictIDs: []string{memoryID}, Priority: 2, Rationale: "duplicate", CreatedAt: time.Now().UTC(),
	}
	if err := triage.PersistCandidateTriage(ctx, tenant.ID, []ports.CandidateTriageRecord{record}); err != nil {
		t.Fatalf("PersistCandidateTriage() error = %v", err)
	}
	latest, err := triage.LatestCandidateTriage(ctx, tenant.ID, []string{memoryID})
	if err != nil {
		t.Fatalf("LatestCandidateTriage() error = %v", err)
	}
	if finding, ok := latest[memoryID]; !ok || finding.DuplicateGroup != "group-1" || finding.Priority != 2 {
		t.Fatalf("latest triage = %#v", latest)
	}

	relationID := "00000000-0000-4000-8000-0000000000e1"
	if err := triage.AddMemoryRelations(ctx, tenant.ID, []ports.MemoryRelationRecord{{
		ID: relationID, SourceMemoryID: memoryID, TargetMemoryID: memoryID2,
		RelationType: "generalizes", Confidence: 0.5, CreatedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("AddMemoryRelations() error = %v", err)
	}
	var relations int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_relations WHERE source_memory_id = $1::uuid AND relation_type = 'generalizes'`, memoryID).Scan(&relations)
	}); err != nil || relations != 1 {
		t.Fatalf("generalizes relations = %d err = %v", relations, err)
	}
}
