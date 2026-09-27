package registry

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/db/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func integrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTenantSchemaMigrationsOnFreshSchema(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := NewStore(db)
	tenant, err := store.RegisterTenant(ctx, "Fresh schema tenant")
	if err != nil {
		t.Fatalf("RegisterTenant() error = %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+tenant.Schema); err != nil {
		t.Fatalf("create tenant schema: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tenant migration: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path TO `+tenant.Schema+`, public`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set tenant migration search_path: %v", err)
	}
	if err := migrations.ApplyTenant(ctx, tx, migrations.TenantFS()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply tenant migrations: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tenant migration: %v", err)
	}

	for _, table := range []string{
		"tenant_migrations", "sessions", "observed_events", "normalized_events", "outbox_jobs",
		"memory_nodes", "memory_evidence", "memory_relations", "memory_feedback", "extraction_runs",
		"analysis_jobs", "retrieval_events", "retrieval_cache", "projection_events", "embedding_jobs", "audit_events",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'r')`, tenant.Schema, table).Scan(&exists); err != nil {
			t.Fatalf("check tenant table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("tenant table %s was not migrated into %s", table, tenant.Schema)
		}
	}
	reapplyTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tenant migration retry: %v", err)
	}
	if _, err := reapplyTx.ExecContext(ctx, `SET LOCAL search_path TO `+tenant.Schema+`, public`); err != nil {
		_ = reapplyTx.Rollback()
		t.Fatalf("set search_path for migration retry: %v", err)
	}
	if err := migrations.ApplyTenant(ctx, reapplyTx, migrations.TenantFS()); err != nil {
		_ = reapplyTx.Rollback()
		t.Fatalf("reapply tenant migrations: %v", err)
	}
	if err := reapplyTx.Commit(); err != nil {
		t.Fatalf("commit tenant migration retry: %v", err)
	}
}

func TestTenantRegistrationAndSchemaRouting(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	// Applying migrations repeatedly must be safe and must not recreate state.
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("reapply platform migrations: %v", err)
	}

	store := NewStore(db)
	tenant, err := store.RegisterTenant(ctx, "Integration tenant")
	if err != nil {
		t.Fatalf("RegisterTenant() error = %v", err)
	}
	if tenant.Status != "provisioning" || !tenantSchemaPattern.MatchString(tenant.Schema) {
		t.Fatalf("registered tenant = %#v", tenant)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})

	if _, err := store.ResolveSchema(ctx, tenant.ID); !errors.Is(err, ErrTenantNotRoutable) {
		t.Fatalf("ResolveSchema before provisioning = %v, want ErrTenantNotRoutable", err)
	}
	if err := store.ActivateTenant(ctx, tenant.ID); !errors.Is(err, ErrTenantNotRoutable) {
		t.Fatalf("ActivateTenant without physical schema = %v, want ErrTenantNotRoutable", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+tenant.Schema); err != nil {
		t.Fatalf("create test tenant schema: %v", err)
	}
	migrationTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tenant migration: %v", err)
	}
	if _, err := migrationTx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, tenant.Schema+", public"); err != nil {
		_ = migrationTx.Rollback()
		t.Fatalf("set tenant migration search_path: %v", err)
	}
	if err := migrations.ApplyTenant(ctx, migrationTx, migrations.TenantFS()); err != nil {
		_ = migrationTx.Rollback()
		t.Fatalf("apply tenant migration: %v", err)
	}
	var migrationVersion int
	if err := migrationTx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM tenant_migrations`).Scan(&migrationVersion); err != nil {
		_ = migrationTx.Rollback()
		t.Fatalf("read tenant migration version: %v", err)
	}
	if _, err := migrationTx.ExecContext(ctx, `UPDATE public.tenant_schema_registry SET migration_version = $1 WHERE tenant_id = $2`, migrationVersion, tenant.ID); err != nil {
		_ = migrationTx.Rollback()
		t.Fatalf("record tenant migration version: %v", err)
	}
	if err := migrationTx.Commit(); err != nil {
		t.Fatalf("commit tenant migration: %v", err)
	}
	if err := store.ActivateTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("ActivateTenant() error = %v", err)
	}
	resolved, err := store.ResolveSchema(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("ResolveSchema() error = %v", err)
	}
	if resolved != tenant.Schema {
		t.Fatalf("resolved schema = %q, want %q", resolved, tenant.Schema)
	}
}
