// Package assembly wires the Mengpo memory service binaries. It owns the
// dependency graph (config -> database -> repositories -> services -> HTTP or
// worker loop) so cmd/memory-server and cmd/memory-worker stay thin and the
// graph can be built and tested without starting a process.
package assembly

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hchw/mengpo/internal/platform/registry"
)

// OpenDB opens and verifies a PostgreSQL connection pool.
func OpenDB(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("database DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// Migrate applies the platform (public schema) migrations. It is idempotent and
// safe to run from every process at startup.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		return err
	}
	// Bring existing tenants up to the latest tenant migrations, not just newly
	// provisioned ones.
	return registry.UpgradeTenantSchemas(ctx, db)
}
