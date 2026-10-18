package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func insertMergeNode(t *testing.T, ctx context.Context, db *sql.DB, schema, id, userID, status string, evidenceIDs []string) {
	t.Helper()
	provenance, err := json.Marshal(map[string]any{"evidence_event_ids": evidenceIDs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.memory_nodes
		(id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, confidence,
		 applicability, content, content_text, default_retrieval, provenance)
		VALUES ($1, $2, $3, 'user-global', $3, 'fact', $4, 0.6, '{}'::jsonb, '{}'::jsonb, $5, false, $6::jsonb)`, schema),
		id, "idem-"+id, userID, status, "text-"+id, string(provenance)); err != nil {
		t.Fatalf("insert node %s: %v", id, err)
	}
}

func insertEvidence(t *testing.T, ctx context.Context, db *sql.DB, schema, eventID, memoryID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.observed_events
		(id, idempotency_key, source_type, source_id, message_type, occurred_at, retention_class)
		VALUES ($1, $2, 'user', 'user-1', 'message', now(), 'standard')`, schema),
		eventID, "idem-"+eventID); err != nil {
		t.Fatalf("insert observed event %s: %v", eventID, err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.memory_evidence
		(id, memory_id, raw_event_id, evidence_role, confidence, attribution)
		VALUES (gen_random_uuid(), $1, $2, 'supports', 0.7, 'direct')`, schema),
		memoryID, eventID); err != nil {
		t.Fatalf("insert evidence %s: %v", eventID, err)
	}
}

func TestMergeMemoriesUnionsEvidenceAndRetiresDuplicates(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "merge evidence")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repo := NewMemoryRepository(router)
	evidenceReader := NewCandidateRepository(router)

	const (
		userID   = "00000000-0000-4000-8000-000000000c01"
		targetID = "00000000-0000-4000-8000-000000000c02"
		dupID    = "00000000-0000-4000-8000-000000000c03"
		targetEv = "00000000-0000-4000-8000-000000000c04"
		dupEv    = "00000000-0000-4000-8000-000000000c05"
	)
	insertMergeNode(t, ctx, db, tenant.Schema, targetID, userID, "candidate", []string{targetEv})
	insertMergeNode(t, ctx, db, tenant.Schema, dupID, userID, "candidate", []string{dupEv})
	insertEvidence(t, ctx, db, tenant.Schema, targetEv, targetID)
	insertEvidence(t, ctx, db, tenant.Schema, dupEv, dupID)

	result, err := repo.MergeMemories(ctx, tenant.ID, ports.MergeRequest{
		TargetID: targetID, DuplicateIDs: []string{dupID}, ActorID: userID, RequestID: "req-1", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("MergeMemories() error = %v", err)
	}
	if result.Target.Status != "active" || !result.Target.DefaultRetrieval {
		t.Fatalf("merged target = %#v, want active and retrievable", result.Target)
	}

	// The target's evidence reader must now see both raw events.
	evidence, err := evidenceReader.ListMemoryEvidence(ctx, tenant.ID, targetID)
	if err != nil {
		t.Fatalf("ListMemoryEvidence() error = %v", err)
	}
	seen := map[string]bool{}
	for _, record := range evidence {
		seen[record.RawEventID] = true
	}
	if !seen[targetEv] || !seen[dupEv] {
		t.Fatalf("target evidence = %#v, want both %s and %s", seen, targetEv, dupEv)
	}

	// The duplicate is retired but retained, linked by merged_into.
	var status string
	var defaultRetrieval bool
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT status, default_retrieval FROM %s.memory_nodes WHERE id=$1`, tenant.Schema), dupID).Scan(&status, &defaultRetrieval); err != nil {
		t.Fatal(err)
	}
	if status != "rejected" || defaultRetrieval {
		t.Fatalf("duplicate = (%s,%v), want (rejected,false)", status, defaultRetrieval)
	}
	var relations int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s.memory_relations WHERE source_memory_id=$1 AND target_memory_id=$2 AND relation_type='merged_into'`, tenant.Schema), dupID, targetID).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 1 {
		t.Fatalf("merged_into relations = %d, want 1", relations)
	}
	var audits int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s.audit_events WHERE action='merge' AND resource_id=$1`, tenant.Schema), targetID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("merge audits = %d, want 1", audits)
	}
}

func TestMergeMemoriesRejectsScopeDrift(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "merge scope drift")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repo := NewMemoryRepository(router)

	const (
		userA = "00000000-0000-4000-8000-000000000d01"
		userB = "00000000-0000-4000-8000-000000000d02"
	)
	insertMergeNode(t, ctx, db, tenant.Schema, "00000000-0000-4000-8000-000000000d03", userA, "candidate", nil)
	insertMergeNode(t, ctx, db, tenant.Schema, "00000000-0000-4000-8000-000000000d04", userB, "candidate", nil)
	_, err = repo.MergeMemories(ctx, tenant.ID, ports.MergeRequest{
		TargetID: "00000000-0000-4000-8000-000000000d03", DuplicateIDs: []string{"00000000-0000-4000-8000-000000000d04"}, ActorID: userA,
	})
	if !errors.Is(err, ports.ErrMergeScopeDrift) {
		t.Fatalf("MergeMemories() error = %v, want ErrMergeScopeDrift", err)
	}
}
