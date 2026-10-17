package assembly

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestSessionBoundarySummaryIsRecallableInSession verifies that a session
// summary produced at a boundary is retrievable during the same session: it is
// active, default-retrievable, and carries a non-empty text projection so the
// full-text channel can find it.
func TestSessionBoundarySummaryIsRecallableInSession(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "session summary recall")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	const userID = "00000000-0000-4000-8000-0000000000d1"
	const sessionID = "00000000-0000-4000-8000-0000000000d2"
	const nodeID = "00000000-0000-4000-8000-0000000000d3"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1::uuid, $2::uuid, 'active')`, sessionID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO memory_nodes (id, idempotency_key, user_id, session_id, scope_type, scope_id, memory_type, status, visibility, confidence, content, content_text, default_retrieval, version, provenance)
VALUES ($1::uuid, 'recall-boundary', $2::uuid, $3::uuid, 'session', $3::uuid, 'session_summary', 'active', 'private', 0.6, '{"summary":"the build needs GOPROXY"}'::jsonb, 'the build needs GOPROXY', true, 1, '{}'::jsonb)`, nodeID, userID, sessionID)
		return err
	}); err != nil {
		t.Fatalf("seed session summary: %v", err)
	}

	service := recall.NewService(
		postgres.NewSearchRepository(router),
		postgres.NewRelationRepository(router),
		postgres.NewMemoryRepository(router),
		postgres.NewEmbeddingRepository(router),
	)
	candidates, _, err := service.RecallWithMeta(ctx, tenant.ID, userID, sessionID, recall.RecallRequest{Query: "GOPROXY"})
	if err != nil {
		t.Fatalf("RecallWithMeta() error = %v", err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.Node.ID == nodeID {
			found = true
		}
	}
	if !found {
		t.Fatalf("session summary not recalled; got %d candidates", len(candidates))
	}
}
