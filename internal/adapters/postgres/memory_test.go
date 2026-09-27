package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/db/migrations"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMemoryRepositoryIdempotencyOptimisticLockAndTenantIsolation(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(8)
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
	tenantA := createMigratedTenant(t, ctx, db, store, "repository tenant A")
	tenantB := createMigratedTenant(t, ctx, db, store, "repository tenant B")
	for _, tenant := range []struct{ id, schema string }{{tenantA.ID, tenantA.Schema}, {tenantB.ID, tenantB.Schema}} {
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.id)
		})
	}

	repository := NewMemoryRepository(tenantdb.NewRouter(db, store))
	original := ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000101", IdempotencyKey: "candidate:run-1:user-1",
		UserID: "00000000-0000-4000-8000-000000000001", ScopeType: "user-global",
		ScopeID: "00000000-0000-4000-8000-000000000001", MemoryType: "preference",
		Status: "candidate", Confidence: 0.6, Applicability: json.RawMessage(`{"when":"always"}`),
		Content: json.RawMessage(`{"text":"prefers concise answers"}`), ContentText: "prefers concise answers",
	}
	created, err := repository.Create(ctx, tenantA.ID, original)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Version != 1 || created.ID != original.ID {
		t.Fatalf("created node = %#v", created)
	}

	replay := original
	replay.ID = "00000000-0000-4000-8000-000000000102"
	replay.Content = json.RawMessage(`{"text":"different retry payload"}`)
	idempotent, err := repository.Create(ctx, tenantA.ID, replay)
	if err != nil {
		t.Fatalf("idempotent Create() error = %v", err)
	}
	if idempotent.ID != created.ID || string(idempotent.Content) != string(created.Content) {
		t.Fatalf("idempotent replay returned %#v, want original %#v", idempotent, created)
	}

	updatedInput := created
	updatedInput.Status = "active"
	updatedInput.Content = json.RawMessage(`{"text":"confirmed preference"}`)
	updated, err := repository.Update(ctx, tenantA.ID, updatedInput, 1)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Version != 2 || updated.Status != "active" {
		t.Fatalf("updated memory = %#v", updated)
	}
	if _, err := repository.Update(ctx, tenantA.ID, updatedInput, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v, want ErrVersionConflict", err)
	}
	if _, err := repository.Get(ctx, tenantA.ID, "00000000-0000-4000-8000-000000000999"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("missing Get() error = %v, want ErrMemoryNotFound", err)
	}
	if _, err := repository.Get(ctx, tenantB.ID, original.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("cross-tenant Get() error = %v, want ErrMemoryNotFound", err)
	}
}

func TestLoadScopeTreeEnforcesScopesAndParentDepth(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(8)
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
	tenant := createMigratedTenant(t, ctx, db, store, "scope tree tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	repository := NewMemoryRepository(tenantdb.NewRouter(db, store))
	userID := "00000000-0000-4000-8000-000000000001"
	otherUserID := "00000000-0000-4000-8000-000000000002"
	sessionID := "00000000-0000-4000-8000-000000000011"
	otherSessionID := "00000000-0000-4000-8000-000000000012"
	if err := tenantdb.NewRouter(db, store).WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		for _, id := range []string{sessionID, otherSessionID} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1, $2, 'active')`, id, userID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	createNode := func(id, key, ownerID, session, scopeType, scopeID, parentID, status string, defaultRetrieval bool) ports.MemoryNodeRecord {
		t.Helper()
		node, err := repository.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
			ID: id, IdempotencyKey: key, UserID: ownerID, SessionID: session,
			ScopeType: scopeType, ScopeID: scopeID, ParentID: parentID, MemoryType: "fact",
			Status: status, Confidence: 0.9, Applicability: json.RawMessage(`{}`),
			Content: json.RawMessage(`{"text":"node"}`), ContentText: "node", DefaultRetrieval: defaultRetrieval,
		})
		if err != nil {
			t.Fatalf("create node %s: %v", id, err)
		}
		return node
	}
	parent := createNode("00000000-0000-4000-8000-000000000201", "tree:parent", userID, "", "user-global", userID, "", "stable", false)
	child := createNode("00000000-0000-4000-8000-000000000202", "tree:child", userID, "", "user-global", userID, parent.ID, "stable", true)
	sessionNode := createNode("00000000-0000-4000-8000-000000000203", "tree:session", userID, sessionID, "session", sessionID, "", "active", true)
	createNode("00000000-0000-4000-8000-000000000204", "tree:other-user", otherUserID, "", "user-global", otherUserID, "", "stable", true)
	createNode("00000000-0000-4000-8000-000000000205", "tree:other-session", userID, otherSessionID, "session", otherSessionID, "", "stable", true)
	createNode("00000000-0000-4000-8000-000000000206", "tree:candidate", userID, "", "user-global", userID, "", "candidate", true)

	if err := tenantdb.NewRouter(db, store).WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_relations (id, source_memory_id, target_memory_id, relation_type) VALUES ($1, $2, $3, 'related')`,
			"00000000-0000-4000-8000-000000000301", child.ID, sessionNode.ID)
		return err
	}); err != nil {
		t.Fatalf("insert relation: %v", err)
	}

	tree, err := repository.LoadScopeTree(ctx, tenant.ID, userID, sessionID, 1, 100)
	if err != nil {
		t.Fatalf("LoadScopeTree() error = %v", err)
	}
	if len(tree.Nodes) != 3 || len(tree.Relations) != 1 {
		t.Fatalf("tree has %d nodes and %d relations, want 3 and 1: %#v", len(tree.Nodes), len(tree.Relations), tree)
	}
	depths := make(map[string]int, len(tree.Nodes))
	for _, node := range tree.Nodes {
		depths[node.ID] = node.ParentDepth
	}
	if depths[child.ID] != 0 || depths[parent.ID] != 1 || depths[sessionNode.ID] != 0 {
		t.Fatalf("unexpected parent depths: %#v", depths)
	}
	shallow, err := repository.LoadScopeTree(ctx, tenant.ID, userID, sessionID, 0, 100)
	if err != nil {
		t.Fatalf("LoadScopeTree(maxDepth=0) error = %v", err)
	}
	if len(shallow.Nodes) != 2 {
		t.Fatalf("depth-limited tree returned %d nodes, want 2", len(shallow.Nodes))
	}
	if _, err := repository.LoadScopeTree(ctx, tenant.ID, userID, sessionID, MaxParentDepth+1, 100); !errors.Is(err, ErrInvalidScopeTree) {
		t.Fatalf("excessive parent depth error = %v, want ErrInvalidScopeTree", err)
	}
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
