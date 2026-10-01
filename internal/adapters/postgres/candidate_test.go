package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestCandidateRepositoryPersistsCandidatesWithEvidence verifies the writer
// stores candidates (never promoted) together with their evidence, idempotently.
func TestCandidateRepositoryPersistsCandidatesWithEvidence(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "candidate repository")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	eventID := "00000000-0000-4000-8000-0000000000aa"
	userID := "00000000-0000-4000-8000-0000000000bb"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO observed_events (id, idempotency_key, source_type, source_id, message_type, occurred_at, retention_class)
VALUES ($1, 'evidence-1', 'user', 'user-src', 'message', now(), 'standard')`, eventID)
		return err
	}); err != nil {
		t.Fatalf("seed observed event: %v", err)
	}

	repository := NewCandidateRepository(router)
	record := ports.CandidateMemoryRecord{
		Node: ports.MemoryNodeRecord{
			ID:               "00000000-0000-4000-8000-0000000000cc",
			IdempotencyKey:   "candidate:run-1:c1",
			UserID:           userID,
			ScopeType:        "user-global",
			ScopeID:          userID,
			MemoryType:       "insight",
			Status:           "candidate",
			Confidence:       0.6,
			Content:          json.RawMessage(`{"text":"prefers concise answers"}`),
			ContentText:      "prefers concise answers",
			DefaultRetrieval: false,
			Provenance:       json.RawMessage(`{"prompt_version":"llm-v1","evidence":["` + eventID + `"]}`),
		},
		Evidence: []ports.MemoryEvidenceRecord{{
			RawEventID:   eventID,
			EvidenceRole: "supports",
			Confidence:   0.6,
			Attribution:  "inferred",
		}},
	}
	inserted, err := repository.PersistCandidates(ctx, tenant.ID, []ports.CandidateMemoryRecord{record})
	if err != nil {
		t.Fatalf("PersistCandidates() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}

	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var status string
		var defaultRetrieval bool
		if err := tx.QueryRowContext(ctx, `SELECT status, default_retrieval FROM memory_nodes WHERE idempotency_key=$1`, "candidate:run-1:c1").Scan(&status, &defaultRetrieval); err != nil {
			return err
		}
		if status != "candidate" || defaultRetrieval {
			return fmt.Errorf("candidate status=%s default_retrieval=%v", status, defaultRetrieval)
		}
		var evidenceCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_evidence WHERE raw_event_id=$1`, eventID).Scan(&evidenceCount); err != nil {
			return err
		}
		if evidenceCount != 1 {
			return fmt.Errorf("evidence count = %d, want 1", evidenceCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Idempotent replay must not duplicate node or evidence.
	replay := record
	replay.Node.ID = "00000000-0000-4000-8000-0000000000dd"
	inserted, err = repository.PersistCandidates(ctx, tenant.ID, []ports.CandidateMemoryRecord{replay})
	if err != nil {
		t.Fatalf("replay PersistCandidates() error = %v", err)
	}
	if inserted != 0 {
		t.Fatalf("replay inserted = %d, want 0", inserted)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var nodes, evidence int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes`).Scan(&nodes); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_evidence`).Scan(&evidence); err != nil {
			return err
		}
		if nodes != 1 || evidence != 1 {
			return fmt.Errorf("after replay nodes=%d evidence=%d, want 1/1", nodes, evidence)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
