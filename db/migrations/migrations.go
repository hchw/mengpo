// Package migrations embeds platform and tenant SQL migrations for reproducible deployment.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed platform/*.sql tenant/*
var migrationFiles embed.FS

func PlatformFS() fs.FS {
	result, err := fs.Sub(migrationFiles, "platform")
	if err != nil {
		panic(err)
	}
	return result
}

func TenantFS() fs.FS {
	result, err := fs.Sub(migrationFiles, "tenant")
	if err != nil {
		panic(err)
	}
	return result
}

func ApplyPlatform(ctx context.Context, db *sql.DB) error {
	return apply(ctx, db, PlatformFS(), "public.platform_migrations")
}

func ApplyTenant(ctx context.Context, tx *sql.Tx, files fs.FS) error {
	return applyTx(ctx, tx, files, "tenant_migrations")
}

// RollbackTenant reverts exactly one latest tenant migration. Callers must enforce
// that rollback is limited to a not-yet-enabled provisioning schema.
func RollbackTenant(ctx context.Context, tx *sql.Tx, files fs.FS) (int, error) {
	if err := migrationLock(ctx, tx, "tenant_migrations"); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tenant_migrations (
		version integer PRIMARY KEY,
		name text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return 0, fmt.Errorf("create tenant migration ledger: %w", err)
	}
	var version int
	var name string
	err := tx.QueryRowContext(ctx, `SELECT version, name FROM tenant_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load latest tenant migration: %w", err)
	}
	downName := strings.TrimSuffix(name, ".sql") + ".down.sql"
	contents, err := fs.ReadFile(files, filepath.ToSlash(downName))
	if err != nil {
		return 0, fmt.Errorf("missing rollback migration %q: %w", downName, err)
	}
	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return 0, fmt.Errorf("rollback tenant migration %q: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tenant_migrations WHERE version = $1`, version); err != nil {
		return 0, fmt.Errorf("remove tenant migration record %q: %w", name, err)
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM tenant_migrations`).Scan(&remaining); err != nil {
		return 0, fmt.Errorf("read remaining tenant migration version: %w", err)
	}
	return remaining, nil
}

func apply(ctx context.Context, db *sql.DB, files fs.FS, ledger string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()
	if err := applyTx(ctx, tx, files, ledger); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func applyTx(ctx context.Context, tx *sql.Tx, files fs.FS, ledger string) error {
	if ledger != "public.platform_migrations" && ledger != "tenant_migrations" {
		return fmt.Errorf("invalid migration ledger %q", ledger)
	}
	if err := migrationLock(ctx, tx, ledger); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+ledger+` (
		version integer PRIMARY KEY,
		name text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create %s ledger: %w", ledger, err)
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return fmt.Errorf("read migration directory: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") && !strings.HasSuffix(entry.Name(), ".down.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		versionText, _, ok := strings.Cut(name, "_")
		if !ok {
			return fmt.Errorf("migration %q must start with a numeric version", name)
		}
		version, err := strconv.Atoi(versionText)
		if err != nil || version <= 0 {
			return fmt.Errorf("migration %q has invalid version", name)
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+ledger+` WHERE version = $1)`, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %q: %w", name, err)
		}
		if exists {
			continue
		}
		contents, err := fs.ReadFile(files, filepath.ToSlash(name))
		if err != nil {
			return fmt.Errorf("read migration %q: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %q: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+ledger+` (version, name) VALUES ($1, $2)`, version, name); err != nil {
			return fmt.Errorf("record migration %q: %w", name, err)
		}
	}
	return nil
}

func migrationLock(ctx context.Context, tx *sql.Tx, name string) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "mengpo:migrations:"+name); err != nil {
		return fmt.Errorf("lock migration ledger %q: %w", name, err)
	}
	return nil
}
