package tenantdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/db/migrations"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type resolverFunc func(context.Context, string) (string, error)

func (f resolverFunc) ResolveSchema(ctx context.Context, tenantID string) (string, error) {
	return f(ctx, tenantID)
}

func TestRouterUsesTenantRegistryAndRestoresPooledSearchPath(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := registry.NewStore(db)

	tenantA := provisionMarkerTenant(t, ctx, db, store, "router tenant A", "marker-A")
	tenantB := provisionMarkerTenant(t, ctx, db, store, "router tenant B", "marker-B")
	for _, tenant := range []struct{ id, schema string }{{tenantA.ID, tenantA.Schema}, {tenantB.ID, tenantB.Schema}} {
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.id)
		})
	}

	router := NewRouter(db, store)
	var before string
	if err := db.QueryRowContext(ctx, `SHOW search_path`).Scan(&before); err != nil {
		t.Fatalf("read initial search_path: %v", err)
	}
	for _, test := range []struct {
		id, want string
	}{{tenantA.ID, "marker-A"}, {tenantB.ID, "marker-B"}} {
		err := router.WithTenantTx(ctx, test.id, func(tx *sql.Tx) error {
			var got string
			if err := tx.QueryRowContext(ctx, `SELECT value FROM tenant_marker`).Scan(&got); err != nil {
				return err
			}
			if got != test.want {
				return fmt.Errorf("tenant query returned %q, want %q", got, test.want)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithTenantTx(%s): %v", test.id, err)
		}
	}
	var after string
	if err := db.QueryRowContext(ctx, `SHOW search_path`).Scan(&after); err != nil {
		t.Fatalf("read final search_path: %v", err)
	}
	if after != before {
		t.Fatalf("pooled connection search_path leaked: before=%q after=%q", before, after)
	}

	rollbackErr := errors.New("force rollback")
	err = router.WithTenantTx(ctx, tenantA.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tenant_marker(value) VALUES ('rolled-back')`)
		if err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithTenantTx rollback error = %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+tenantA.Schema+`.tenant_marker WHERE value = 'rolled-back'`).Scan(&count); err != nil {
		t.Fatalf("verify rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back write persisted: count=%d", count)
	}
}

func TestRouterRejectsUnsafeSchemaFromResolver(t *testing.T) {
	router := NewRouter(nil, resolverFunc(func(context.Context, string) (string, error) {
		return `tenant_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; DROP SCHEMA public; --`, nil
	}))
	err := router.WithTenantTx(context.Background(), "tenant-id", func(*sql.Tx) error { return nil })
	if !errors.Is(err, ErrInvalidSchemaName) {
		t.Fatalf("unsafe schema error = %v, want ErrInvalidSchemaName", err)
	}
}

func provisionMarkerTenant(t *testing.T, ctx context.Context, db *sql.DB, store *registry.Store, name, marker string) auth.Tenant {
	t.Helper()
	tenant, err := store.RegisterTenant(ctx, name)
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+tenant.Schema); err != nil {
		t.Fatalf("create schema %s: %v", tenant.Schema, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tenant setup: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, tenant.Schema+", public"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set tenant search_path: %v", err)
	}
	if err := migrations.ApplyTenant(ctx, tx, migrations.TenantFS()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply tenant migration: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE tenant_marker (value text NOT NULL)`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("create marker table: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_marker(value) VALUES ($1)`, marker); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert marker: %v", err)
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
		t.Fatalf("commit tenant setup: %v", err)
	}
	if err := store.ActivateTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("activate %s: %v", name, err)
	}
	return tenant
}
