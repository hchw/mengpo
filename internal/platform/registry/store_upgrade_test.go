package registry

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestUpgradeTenantSchemasAppliesPendingMigrations proves an already-provisioned
// tenant is upgraded to the latest tenant migrations at startup, which is what
// keeps existing tenants working after a deploy adds new columns.
func TestUpgradeTenantSchemasAppliesPendingMigrations(t *testing.T) {
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
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	tenant, err := store.ProvisionTenant(ctx, "upgrade tenant")
	if err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})

	// Simulate a tenant provisioned before the latest migration existed.
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+tenant.Schema+`.analysis_jobs DROP COLUMN IF EXISTS produced_memory_ids`); err != nil {
		t.Fatalf("drop new column: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM `+tenant.Schema+`.tenant_migrations WHERE version = 6`); err != nil {
		t.Fatalf("forget migration ledger entry: %v", err)
	}

	if err := UpgradeTenantSchemas(ctx, db); err != nil {
		t.Fatalf("UpgradeTenantSchemas() error = %v", err)
	}

	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='analysis_jobs' AND column_name='produced_memory_ids')`, tenant.Schema).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("pending tenant migration was not applied to the existing tenant")
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT migration_version FROM public.tenant_schema_registry WHERE tenant_id=$1`, tenant.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 6 {
		t.Fatalf("registry migration_version = %d, want >= 6", version)
	}

	// Idempotent: a second pass is a no-op.
	if err := UpgradeTenantSchemas(ctx, db); err != nil {
		t.Fatalf("second UpgradeTenantSchemas() error = %v", err)
	}
}
