package registry

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/db/migrations"
	"github.com/hchw/mengpo/internal/domain/auth"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var tenantMigrationFile = regexp.MustCompile(`^[0-9]{6}_.+\.sql$`)

func tenantMigrationCount(t *testing.T) int {
	t.Helper()
	entries, err := fs.ReadDir(migrations.TenantFS(), ".")
	if err != nil {
		t.Fatalf("list tenant migrations: %v", err)
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && tenantMigrationFile.MatchString(name) && !strings.HasSuffix(name, ".down.sql") {
			count++
		}
	}
	if count == 0 {
		t.Fatal("no tenant migrations found")
	}
	return count
}

func TestTenantLifecycleProvisionPauseAndDelete(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := NewStore(db)
	tenant, err := store.ProvisionTenant(ctx, "lifecycle tenant")
	if err != nil {
		t.Fatalf("ProvisionTenant() error = %v", err)
	}
	if tenant.Status != auth.TenantActive {
		t.Fatalf("provisioned tenant status = %q, want active", tenant.Status)
	}
	t.Cleanup(func() {
		_ = store.PauseTenant(context.Background(), tenant.ID)
		_ = store.DeleteTenant(context.Background(), tenant.ID)
	})
	resolved, err := store.ResolveSchema(ctx, tenant.ID)
	if err != nil || resolved != tenant.Schema {
		t.Fatalf("ResolveSchema() = %q, %v, want %q", resolved, err, tenant.Schema)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT migration_version FROM public.tenant_schema_registry WHERE tenant_id = $1`, tenant.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if want := tenantMigrationCount(t); version != want {
		t.Fatalf("tenant migration version = %d, want %d", version, want)
	}
	if err := store.PauseTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("PauseTenant() error = %v", err)
	}
	if _, err := store.ResolveSchema(ctx, tenant.ID); !errors.Is(err, ErrTenantNotRoutable) {
		t.Fatalf("ResolveSchema after pause = %v, want ErrTenantNotRoutable", err)
	}
	if err := store.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("DeleteTenant() error = %v", err)
	}
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)`, tenant.Schema).Scan(&schemaExists); err != nil {
		t.Fatal(err)
	}
	if schemaExists {
		t.Fatalf("tenant schema %q remains after deletion", tenant.Schema)
	}
	if _, err := store.ResolveSchema(ctx, tenant.ID); !errors.Is(err, ErrTenantNotRoutable) {
		t.Fatalf("ResolveSchema after deletion = %v, want ErrTenantNotRoutable", err)
	}
}

func TestMigrationRollbackIsLimitedToProvisioningTenants(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := NewStore(db)
	tenant, err := store.RegisterTenant(ctx, "rollback tenant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteTenant(context.Background(), tenant.ID) })
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+tenant.Schema); err != nil {
		t.Fatalf("create provisioning schema: %v", err)
	}
	applyTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyTx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, tenant.Schema+", public"); err != nil {
		_ = applyTx.Rollback()
		t.Fatal(err)
	}
	if err := migrations.ApplyTenant(ctx, applyTx, migrations.TenantFS()); err != nil {
		_ = applyTx.Rollback()
		t.Fatalf("apply tenant migrations: %v", err)
	}
	if _, err := applyTx.ExecContext(ctx, `UPDATE public.tenant_schema_registry SET migration_version = $2 WHERE tenant_id = $1`, tenant.ID, tenantMigrationCount(t)); err != nil {
		_ = applyTx.Rollback()
		t.Fatal(err)
	}
	if err := applyTx.Commit(); err != nil {
		t.Fatal(err)
	}

	expectedVersions := make([]int, 0, tenantMigrationCount(t))
	for version := tenantMigrationCount(t) - 1; version >= 0; version-- {
		expectedVersions = append(expectedVersions, version)
	}
	for _, expected := range expectedVersions {
		version, err := store.RollbackProvisioningTenant(ctx, tenant.ID)
		if err != nil {
			t.Fatalf("RollbackProvisioningTenant() error = %v", err)
		}
		if version != expected {
			t.Fatalf("remaining migration version = %d, want %d", version, expected)
		}
	}
	var hasMemoryNodes bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = 'memory_nodes')`, tenant.Schema).Scan(&hasMemoryNodes); err != nil {
		t.Fatal(err)
	}
	if hasMemoryNodes {
		t.Fatal("base migration rollback left tenant memory tables behind")
	}
	if err := store.ActivateTenant(ctx, tenant.ID); !errors.Is(err, ErrTenantNotRoutable) {
		t.Fatalf("activation after full rollback = %v, want ErrTenantNotRoutable", err)
	}
}

func TestMigrationRollbackRejectsEnabledTenant(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	tenant, err := store.ProvisionTenant(ctx, "no rollback active tenant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.PauseTenant(context.Background(), tenant.ID)
		_ = store.DeleteTenant(context.Background(), tenant.ID)
	})
	if _, err := store.RollbackProvisioningTenant(ctx, tenant.ID); !errors.Is(err, ErrTenantNotRoutable) {
		t.Fatalf("rollback enabled tenant = %v, want ErrTenantNotRoutable", err)
	}
}

// TestRestoreReappliesMigrations drills the restore procedure: a schema that is
// already migrated can be re-migrated (as after restoring a dump) without error
// and without changing the recorded version. Re-application is idempotent.
func TestRestoreReappliesMigrations(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	tenant, err := store.ProvisionTenant(ctx, "restore drill tenant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.PauseTenant(context.Background(), tenant.ID)
		_ = store.DeleteTenant(context.Background(), tenant.ID)
	})
	want := tenantMigrationCount(t)
	readVersion := func() int {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, tenant.Schema+", public"); err != nil {
			t.Fatal(err)
		}
		if err := migrations.ApplyTenant(ctx, tx, migrations.TenantFS()); err != nil {
			t.Fatalf("re-apply tenant migrations: %v", err)
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM tenant_migrations`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	if got := readVersion(); got != want {
		t.Fatalf("re-apply version = %d, want %d", got, want)
	}
	if got := readVersion(); got != want {
		t.Fatalf("second re-apply version = %d, want %d", got, want)
	}
}
