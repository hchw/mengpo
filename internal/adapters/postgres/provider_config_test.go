package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestProviderConfigRepositoryIsolatesAndStoresCiphertext proves tenant
// isolation and that the stored secret is ciphertext, not plaintext.
func TestProviderConfigRepositoryIsolatesAndStoresCiphertext(t *testing.T) {
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
	tenantA := createMigratedTenant(t, ctx, db, store, "provider tenant A")
	tenantB := createMigratedTenant(t, ctx, db, store, "provider tenant B")
	t.Cleanup(func() {
		for _, tenant := range []struct{ id, schema string }{{tenantA.ID, tenantA.Schema}, {tenantB.ID, tenantB.Schema}} {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.id)
		}
	})

	repository := NewProviderConfigRepository(tenantdb.NewRouter(db, store))
	ciphertext := []byte{0x00, 0x01, 0x02, 0x03, 0x04}
	if err := repository.Upsert(ctx, tenantA.ID, ports.ProviderConfigRecord{
		Provider: "memory-llm", Enabled: true, BaseURL: "https://tenant-a.example", Model: "m",
		SecretCiphertext: ciphertext, KeyVersion: 1, UpdatedBy: tenantA.ID,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	record, found, err := repository.Get(ctx, tenantA.ID, "memory-llm")
	if err != nil || !found {
		t.Fatalf("Get(A) found=%v err=%v", found, err)
	}
	if record.BaseURL != "https://tenant-a.example" || string(record.SecretCiphertext) != string(ciphertext) {
		t.Fatalf("record = %+v", record)
	}
	// Tenant B is isolated.
	if _, found, err := repository.Get(ctx, tenantB.ID, "memory-llm"); err != nil || found {
		t.Fatalf("tenant B saw tenant A's config: found=%v err=%v", found, err)
	}
	// Deleting removes it.
	if err := repository.Delete(ctx, tenantA.ID, "memory-llm"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, found, err := repository.Get(ctx, tenantA.ID, "memory-llm"); err != nil || found {
		t.Fatalf("config still present after delete: found=%v err=%v", found, err)
	}
}

// TestProviderSecretsAreDestroyedWithTenant proves the provider configuration
// (and its encrypted secret) lives in the tenant schema and disappears when the
// tenant schema is dropped.
func TestProviderSecretsAreDestroyedWithTenant(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "provider destroy")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	repository := NewProviderConfigRepository(tenantdb.NewRouter(db, store))
	if err := repository.Upsert(ctx, tenant.ID, ports.ProviderConfigRecord{Provider: "memory-llm", Enabled: true, BaseURL: "https://x", Model: "m", SecretCiphertext: []byte{1, 2, 3}}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP SCHEMA `+tenant.Schema+` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	var table sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, tenant.Schema+".provider_configs").Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table.Valid {
		t.Fatalf("provider_configs survived tenant schema drop: %s", table.String)
	}
}
