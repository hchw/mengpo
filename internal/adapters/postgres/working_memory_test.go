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

	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestWorkingMemoryRepositoryPersistsActiveSessionMemory verifies the dedicated
// working-memory writer stores session-scoped memory as active, with evidence,
// and stays idempotent. This is the path that broke when session working memory
// was routed through the candidate-only writer.
func TestWorkingMemoryRepositoryPersistsActiveSessionMemory(t *testing.T) {
	db, ctx, tenant, router := workingMemoryTestTenant(t)
	_ = db
	eventID := "00000000-0000-4000-8000-0000000000aa"
	userID := "00000000-0000-4000-8000-0000000000bb"
	sessionID := "00000000-0000-4000-8000-0000000000ee"
	seedWorkingMemorySession(t, ctx, router, tenant.ID, sessionID, userID, eventID)

	repository := NewWorkingMemoryRepository(router)
	record := ports.WorkingMemoryRecord{
		Node: ports.MemoryNodeRecord{
			ID:               "00000000-0000-4000-8000-0000000000cc",
			IdempotencyKey:   "working:run-1:w1",
			UserID:           userID,
			SessionID:        sessionID,
			ScopeType:        "session",
			ScopeID:          sessionID,
			MemoryType:       "insight",
			Status:           "active",
			Confidence:       0.6,
			Content:          json.RawMessage(`{"text":"the build needs GOPROXY set"}`),
			ContentText:      "the build needs GOPROXY set",
			DefaultRetrieval: true,
			Provenance:       json.RawMessage(`{"prompt_version":"rule-v1","evidence":["` + eventID + `"]}`),
		},
		Evidence: []ports.MemoryEvidenceRecord{{
			RawEventID:   eventID,
			EvidenceRole: "supports",
			Confidence:   0.6,
			Attribution:  "inferred",
		}},
	}
	inserted, err := repository.PersistWorkingMemory(ctx, tenant.ID, []ports.WorkingMemoryRecord{record})
	if err != nil {
		t.Fatalf("PersistWorkingMemory() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}

	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var status string
		var defaultRetrieval bool
		if err := tx.QueryRowContext(ctx, `SELECT status, default_retrieval FROM memory_nodes WHERE idempotency_key=$1`, "working:run-1:w1").Scan(&status, &defaultRetrieval); err != nil {
			return err
		}
		if status != "active" || !defaultRetrieval {
			return fmt.Errorf("working memory status=%s default_retrieval=%v", status, defaultRetrieval)
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
	inserted, err = repository.PersistWorkingMemory(ctx, tenant.ID, []ports.WorkingMemoryRecord{replay})
	if err != nil {
		t.Fatalf("replay PersistWorkingMemory() error = %v", err)
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

// TestWorkingMemoryRepositoryEnforcesScope guards the split: working memory is
// session scope and active only, and a writer cannot place it under another
// user's session. The user-global tree must go through the candidate writer.
func TestWorkingMemoryRepositoryEnforcesScope(t *testing.T) {
	db, ctx, tenant, router := workingMemoryTestTenant(t)
	_ = db
	eventID := "00000000-0000-4000-8000-0000000000aa"
	userID := "00000000-0000-4000-8000-0000000000bb"
	sessionID := "00000000-0000-4000-8000-0000000000ee"
	seedWorkingMemorySession(t, ctx, router, tenant.ID, sessionID, userID, eventID)

	repository := NewWorkingMemoryRepository(router)
	evidence := []ports.MemoryEvidenceRecord{{RawEventID: eventID, EvidenceRole: "supports", Confidence: 0.6}}
	base := func() ports.WorkingMemoryRecord {
		return ports.WorkingMemoryRecord{Node: ports.MemoryNodeRecord{
			ID:               "00000000-0000-4000-8000-0000000000cc",
			IdempotencyKey:   "working:guarded",
			UserID:           userID,
			SessionID:        sessionID,
			ScopeType:        "session",
			ScopeID:          sessionID,
			MemoryType:       "insight",
			Status:           "active",
			Confidence:       0.6,
			Content:          json.RawMessage(`{"text":"x"}`),
			ContentText:      "x",
			DefaultRetrieval: true,
		}, Evidence: evidence}
	}

	userGlobal := base()
	userGlobal.Node.ScopeType = "user-global"
	userGlobal.Node.ScopeID = userID
	userGlobal.Node.SessionID = ""
	if _, err := repository.PersistWorkingMemory(ctx, tenant.ID, []ports.WorkingMemoryRecord{userGlobal}); !errors.Is(err, ErrInvalidMemory) {
		t.Fatalf("user-global scope error = %v, want ErrInvalidMemory", err)
	}

	candidateStatus := base()
	candidateStatus.Node.Status = "candidate"
	candidateStatus.Node.DefaultRetrieval = false
	if _, err := repository.PersistWorkingMemory(ctx, tenant.ID, []ports.WorkingMemoryRecord{candidateStatus}); !errors.Is(err, ErrInvalidMemory) {
		t.Fatalf("candidate status error = %v, want ErrInvalidMemory", err)
	}

	foreign := base()
	foreign.Node.UserID = "00000000-0000-4000-8000-0000000000ff"
	if _, err := repository.PersistWorkingMemory(ctx, tenant.ID, []ports.WorkingMemoryRecord{foreign}); !errors.Is(err, ErrMemoryScopeMismatch) {
		t.Fatalf("foreign owner error = %v, want ErrMemoryScopeMismatch", err)
	}
}

func workingMemoryTestTenant(t *testing.T) (*sql.DB, context.Context, auth.Tenant, *tenantdb.Router) {
	t.Helper()
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
	t.Cleanup(cancel)
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "working memory repository")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	return db, ctx, tenant, tenantdb.NewRouter(db, store)
}

func seedWorkingMemorySession(t *testing.T, ctx context.Context, router *tenantdb.Router, tenantID, sessionID, userID, eventID string) {
	t.Helper()
	if err := router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1, $2, 'active')`, sessionID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO observed_events (id, idempotency_key, source_type, source_id, message_type, occurred_at, retention_class)
VALUES ($1, 'evidence-working-1', 'workflow', 'workflow-src', 'context.compaction', now(), 'standard')`, eventID)
		return err
	}); err != nil {
		t.Fatalf("seed working memory fixture: %v", err)
	}
}
