package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/config"
)

func lookupMap(values map[string]string) config.LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

// TestRunServerRejectsIncompleteConfig proves the startup path validates
// configuration before any database work and fails fast on a missing DSN.
func TestRunServerRejectsIncompleteConfig(t *testing.T) {
	err := RunServer(context.Background(), lookupMap(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("RunServer() error = %v, want missing database configuration", err)
	}
}

// TestRunServerAdvancesPastValidConfig proves a complete configuration is
// accepted and the startup path proceeds to the (unreachable) database instead
// of failing configuration validation.
func TestRunServerAdvancesPastValidConfig(t *testing.T) {
	err := RunServer(context.Background(), lookupMap(map[string]string{
		"MEMORY_DATABASE_URL": "postgres://user:pass@127.0.0.1:1/mengpo?sslmode=disable&connect_timeout=1",
	}))
	if err == nil {
		t.Fatal("RunServer() against an unreachable database should fail")
	}
	if strings.Contains(err.Error(), "required") {
		t.Fatalf("RunServer() rejected a complete configuration: %v", err)
	}
	if !strings.Contains(err.Error(), "database") {
		t.Fatalf("RunServer() error = %v, want a database connection error", err)
	}
}

// TestRunWorkerRejectsIncompleteConfig mirrors the server startup guard.
func TestRunWorkerRejectsIncompleteConfig(t *testing.T) {
	err := RunWorker(context.Background(), lookupMap(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("RunWorker() error = %v, want missing database configuration", err)
	}
}
