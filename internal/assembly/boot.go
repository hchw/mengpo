package assembly

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/hchw/mengpo/internal/config"
)

// RunServer is the testable server startup sequence: load and validate
// configuration, connect, migrate, assemble, then serve until cancellation.
func RunServer(ctx context.Context, lookup config.LookupEnv) error {
	cfg, err := config.LoadFrom(lookup)
	if err != nil {
		return err
	}
	db, err := OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if cfg.MigrateOnStart {
		if err := Migrate(ctx, db); err != nil {
			return err
		}
	}
	if cfg.DevSeed && cfg.Environment != "production" {
		if err := DevSeed(ctx, db, slog.New(slog.NewJSONHandler(os.Stderr, nil))); err != nil {
			return err
		}
	}
	server, err := NewServer(cfg, db)
	if err != nil {
		return err
	}
	return server.Run(ctx)
}

// RunWorker is the testable worker startup sequence.
func RunWorker(ctx context.Context, lookup config.LookupEnv) error {
	cfg, err := config.LoadFrom(lookup)
	if err != nil {
		return err
	}
	db, err := OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if cfg.MigrateOnStart {
		if err := Migrate(ctx, db); err != nil {
			return err
		}
	}
	worker, err := newWorkerFromConfig(cfg, db)
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}

// RunMainServer runs the server against the process environment, exiting with a
// fatal error on failure.
func RunMainServer(ctx context.Context) {
	if err := RunServer(ctx, os.LookupEnv); err != nil {
		log.Fatalf("memory server: %v", err)
	}
}

// RunMainWorker runs the worker against the process environment.
func RunMainWorker(ctx context.Context) {
	if err := RunWorker(ctx, os.LookupEnv); err != nil {
		log.Fatalf("memory worker: %v", err)
	}
}
