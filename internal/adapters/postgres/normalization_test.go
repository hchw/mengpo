package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestLoadAnalysisEventsByRawIDsScansAllColumns guards the periodic
// consolidation reader: its query returns the source event's attribution and
// sequence, and a Scan target-count mismatch previously made every consolidate
// job dead-letter with "expected 7 destination arguments in Scan, not 6".
func TestLoadAnalysisEventsByRawIDsScansAllColumns(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "normalized event reader")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	const userID = "00000000-0000-4000-8000-0000000000b1"
	const sessionID = "00000000-0000-4000-8000-0000000000b2"
	const rawEventID = "00000000-0000-4000-8000-0000000000b3"
	const normalizedID = "00000000-0000-4000-8000-0000000000b4"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status, title) VALUES ($1::uuid, $2::uuid, 'active', 'reader')`, sessionID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO observed_events (id, idempotency_key, session_id, source_type, source_id, message_type, payload, occurred_at, retention_class, attribution_level)
VALUES ($1::uuid, 'reader-event', $2::uuid, 'workflow', 'src', 'context.compaction', '{"summary":"distilled span"}'::jsonb, now(), 'standard', 'correlated')`, rawEventID, sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO normalized_events (id, raw_event_id, schema_version, normalized_payload, normalization_version, sequence, processing_status)
VALUES ($1::uuid, $2::uuid, 'v1', '{"data":{"summary":"distilled span"}}'::jsonb, 'v1', 3, 'normalized')`, normalizedID, rawEventID)
		return err
	}); err != nil {
		t.Fatalf("seed normalized event fixtures: %v", err)
	}

	repository := NewNormalizedEventRepository(router)
	events, err := repository.LoadAnalysisEventsByRawIDs(ctx, tenant.ID, []string{rawEventID})
	if err != nil {
		t.Fatalf("LoadAnalysisEventsByRawIDs() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("LoadAnalysisEventsByRawIDs() returned %d events, want 1", len(events))
	}
	got := events[0]
	if got.NormalizedID != normalizedID || got.RawEventID != rawEventID || got.SessionID != sessionID {
		t.Fatalf("identity fields = %#v", got)
	}
	if got.Attribution != "correlated" {
		t.Fatalf("Attribution = %q, want correlated", got.Attribution)
	}
	if got.Sequence == nil || *got.Sequence != 3 {
		t.Fatalf("Sequence = %v, want 3", got.Sequence)
	}
	if len(got.Payload) == 0 {
		t.Fatal("Payload must be carried through")
	}
}
