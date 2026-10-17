package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestTenantCacheKeyEmbedsTenantAndRejectsEmpty proves the key contract the
// database-backed cache depends on, independent of any live database.
func TestTenantCacheKeyEmbedsTenantAndRejectsEmpty(t *testing.T) {
	key, err := ports.TenantCacheKey("tenant-a", "cache", "consolidate:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if key != "mengpo:tenant:tenant-a:cache:consolidate:deadbeef" {
		t.Fatalf("key = %q", key)
	}
	other, _ := ports.TenantCacheKey("tenant-b", "cache", "consolidate:deadbeef")
	if key == other {
		t.Fatal("keys for different tenants collided")
	}
	if _, err := ports.TenantCacheKey("", "cache", "consolidate:deadbeef"); !errors.Is(err, ports.ErrMissingTenantIdentity) {
		t.Fatalf("empty tenant err = %v", err)
	}
}

// TestTenantCacheIsolatesTenantsForSameLogicalKey proves the replacement cache
// keeps the isolation guarantee the external store provided: the same logical
// key resolves inside each tenant's own schema and never crosses tenants.
func TestTenantCacheIsolatesTenantsForSameLogicalKey(t *testing.T) {
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
	tenantA := createMigratedTenant(t, ctx, db, store, "cache isolation a")
	tenantB := createMigratedTenant(t, ctx, db, store, "cache isolation b")
	for _, tenant := range []auth.Tenant{tenantA, tenantB} {
		schema, id := tenant.Schema, tenant.ID
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, id)
		})
	}

	cache := NewTenantCache(tenantdb.NewRouter(db, store))
	if err := cache.Put(ctx, tenantA.ID, "shared-key", []byte(`"a-value"`), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(ctx, tenantB.ID, "shared-key", []byte(`"b-value"`), time.Minute); err != nil {
		t.Fatal(err)
	}
	a, ok, err := cache.Get(ctx, tenantA.ID, "shared-key")
	if err != nil || !ok || string(a) != `"a-value"` {
		t.Fatalf("tenant-a read=%q ok=%v err=%v", a, ok, err)
	}
	b, ok, err := cache.Get(ctx, tenantB.ID, "shared-key")
	if err != nil || !ok || string(b) != `"b-value"` {
		t.Fatalf("tenant-b read=%q ok=%v err=%v", b, ok, err)
	}
	if err := cache.Delete(ctx, tenantA.ID, "shared-key"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cache.Get(ctx, tenantA.ID, "shared-key"); ok {
		t.Fatal("tenant-a value survived delete")
	}
	if _, ok, _ := cache.Get(ctx, tenantB.ID, "shared-key"); !ok {
		t.Fatal("tenant-a delete removed tenant-b value")
	}
	// An unregistered tenant must never fall back to a shared namespace.
	if _, _, err := cache.Get(ctx, "not-a-tenant", "shared-key"); err == nil {
		t.Fatal("read for an unknown tenant was accepted")
	}
	if err := cache.Put(ctx, tenantA.ID, "k", []byte(`"v"`), 0); err == nil {
		t.Fatal("non-positive ttl accepted")
	}
}

// TestTenantCacheExpiresAndPurges covers the TTL contract: an expired entry is a
// miss, and the maintenance purge removes it.
func TestTenantCacheExpiresAndPurges(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "cache expiry")
	schema, id := tenant.Schema, tenant.ID
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, id)
	})

	cache := NewTenantCache(tenantdb.NewRouter(db, store))
	if err := cache.Put(ctx, tenant.ID, "expiring", []byte(`"v"`), 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, ok, err := cache.Get(ctx, tenant.ID, "expiring"); err != nil || ok {
		t.Fatalf("expired entry ok=%v err=%v, want a miss", ok, err)
	}
	removed, err := cache.PurgeExpired(ctx, tenant.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("purged %d expired entries, want 1", removed)
	}
}
