package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestContentTextBackfillRepairsLegacyNodes verifies the repair path: a legacy
// node with structured content and no projection is found and rendered, an
// empty-content node is left alone, and a second refresh is a no-op.
func TestContentTextBackfillRepairsLegacyNodes(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "content text backfill")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	const userID = "00000000-0000-4000-8000-0000000000e1"
	const structuredID = "00000000-0000-4000-8000-0000000000e2"
	const emptyID = "00000000-0000-4000-8000-0000000000e3"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_nodes (id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, visibility, confidence, content, content_text, default_retrieval, version, provenance)
VALUES ($1::uuid, 'backfill-structured', $2::uuid, 'user-global', $2::uuid, 'insight', 'active', 'private', 0.6, '{"name":"log-trace-mcp","type":"project_overview"}'::jsonb, '', true, 1, '{}'::jsonb)`, structuredID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_nodes (id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, visibility, confidence, content, content_text, default_retrieval, version, provenance)
VALUES ($1::uuid, 'backfill-empty', $2::uuid, 'user-global', $2::uuid, 'insight', 'active', 'private', 0.6, '{}'::jsonb, '', true, 1, '{}'::jsonb)`, emptyID, userID)
		return err
	}); err != nil {
		t.Fatalf("seed backfill fixtures: %v", err)
	}

	repository := NewMemoryRepository(router)
	nodes, err := repository.ListMissingContentText(ctx, tenant.ID, 10)
	if err != nil {
		t.Fatalf("ListMissingContentText() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != structuredID {
		t.Fatalf("missing content text nodes = %#v, want only structured", nodes)
	}
	text := memory.RenderContentText(nodes[0].Content)
	if text == "" {
		t.Fatal("renderer produced empty text for structured content")
	}
	updated, err := repository.RefreshContentText(ctx, tenant.ID, structuredID, text)
	if err != nil || !updated {
		t.Fatalf("RefreshContentText() updated=%v err=%v", updated, err)
	}
	updated, err = repository.RefreshContentText(ctx, tenant.ID, structuredID, text)
	if err != nil || updated {
		t.Fatalf("second RefreshContentText() updated=%v err=%v, want false", updated, err)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT content_text FROM memory_nodes WHERE id=$1::uuid`, structuredID).Scan(&stored); err != nil {
			return err
		}
		if stored == "" {
			t.Fatal("structured node projection was not persisted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var emptyStored string
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT content_text FROM memory_nodes WHERE id=$1::uuid`, emptyID).Scan(&emptyStored)
	}); err != nil {
		t.Fatal(err)
	}
	if emptyStored != "" {
		t.Fatalf("empty content node must stay empty, got %q", emptyStored)
	}
}
