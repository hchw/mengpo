package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestQuarantineStoresUnboundEventOutsideTenantSchemas(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	repository := NewQuarantineRepository(db)
	event := ports.QuarantinedEvent{ID: "00000000-0000-4000-8000-000000000201", Reason: "missing-tenant-credential", DeclaredTenant: "tenant-a", PrincipalType: "agent", PrincipalID: "agent-a", RequestID: "req-1", Payload: json.RawMessage(`{"text":"poison"}`)}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM quarantined_events WHERE id=$1`, event.ID)
	})
	if err := repository.QuarantineEvent(ctx, event); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if err := repository.QuarantineEvent(ctx, event); err != nil {
		t.Fatalf("idempotent quarantine: %v", err)
	}
	var count int
	var reason, payload string
	if err := db.QueryRowContext(ctx, `SELECT count(*), min(reason), min(payload::text) FROM quarantined_events WHERE id=$1`, event.ID).Scan(&count, &reason, &payload); err != nil {
		t.Fatal(err)
	}
	if count != 1 || reason != "missing-tenant-credential" || !json.Valid([]byte(payload)) {
		t.Fatalf("quarantined row count=%d reason=%q payload=%q", count, reason, payload)
	}
}
