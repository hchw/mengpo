package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestTenantScheduleStoreIsolatesTenants proves one tenant's cadence change
// never leaks into another tenant.
func TestTenantScheduleStoreIsolatesTenants(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenantA := createMigratedTenant(t, ctx, db, store, "schedule tenant A")
	tenantB := createMigratedTenant(t, ctx, db, store, "schedule tenant B")
	t.Cleanup(func() {
		for _, tenant := range []struct{ id, schema string }{{tenantA.ID, tenantA.Schema}, {tenantB.ID, tenantB.Schema}} {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenant_schedules WHERE tenant_id=$1`, tenant.id)
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.id)
		}
	})

	schedules := NewTenantScheduleStore(db)
	if _, ok, err := schedules.Load(ctx, tenantA.ID, "consolidate"); err != nil || ok {
		t.Fatalf("initial Load = ok:%v err:%v, want no override", ok, err)
	}
	if err := schedules.Upsert(ctx, tenantA.ID, "consolidate", 6*time.Hour, false); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	override, ok, err := schedules.Load(ctx, tenantA.ID, "consolidate")
	if err != nil || !ok {
		t.Fatalf("Load(A) = ok:%v err:%v", ok, err)
	}
	if override.Cadence != 6*time.Hour || override.Enabled {
		t.Fatalf("override = %+v", override)
	}
	// Tenant B is unaffected.
	if _, ok, err := schedules.Load(ctx, tenantB.ID, "consolidate"); err != nil || ok {
		t.Fatalf("tenant B saw an override: ok:%v err:%v", ok, err)
	}
}
