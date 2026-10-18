package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/db/migrations"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func ownerTestDatabase(t *testing.T) (*sql.DB, *registry.Store) {
	t.Helper()
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
	return db, registry.NewStore(db)
}

// TestObservationOwnerMigrationBackfillsFromSession proves the owner migration
// adds the column and backfills it from the session for pre-existing events.
func TestObservationOwnerMigrationBackfillsFromSession(t *testing.T) {
	db, store := ownerTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenant := createMigratedTenant(t, ctx, db, store, "observation owner migration")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})

	const (
		userID    = "00000000-0000-4000-8000-0000000000e1"
		sessionID = "00000000-0000-4000-8000-0000000000e2"
		eventID   = "00000000-0000-4000-8000-0000000000e3"
	)

	// Roll back the owner migration to recreate the pre-anchor schema.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path TO `+tenant.Schema+`, public`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.RollbackTenant(ctx, tx, migrations.TenantFS()); err != nil {
		t.Fatalf("rollback owner migration: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Insert data as it existed before the anchor: a session-bound event with no owner.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.sessions (id, user_id, status) VALUES ($1::uuid, $2::uuid, 'active')`, tenant.Schema), sessionID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.observed_events (id, idempotency_key, session_id, source_type, source_id, message_type, occurred_at, retention_class) VALUES ($1::uuid, $2, $3::uuid, 'user', 'user', 'message', now(), 'standard')`, tenant.Schema), eventID, "idem-"+eventID, sessionID); err != nil {
		t.Fatal(err)
	}

	// Re-apply: the column returns and the backfill fills the owner from the session.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.ExecContext(ctx, `SET LOCAL search_path TO `+tenant.Schema+`, public`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.ApplyTenant(ctx, tx2, migrations.TenantFS()); err != nil {
		t.Fatalf("re-apply tenant migrations: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	var owner sql.NullString
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT user_id::text FROM %s.observed_events WHERE id=$1::uuid`, tenant.Schema), eventID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if !owner.Valid || owner.String != userID {
		t.Fatalf("backfilled owner = %v, want %s", owner, userID)
	}
}

// TestObservationOwnerRoundTripAndAnalysis proves the owner survives storage,
// participates in idempotency, and reaches the periodic analysis readers.
func TestObservationOwnerRoundTripAndAnalysis(t *testing.T) {
	db, store := ownerTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenant := createMigratedTenant(t, ctx, db, store, "observation owner round trip")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})

	const (
		userID    = "00000000-0000-4000-8000-0000000000d1"
		otherUser = "00000000-0000-4000-8000-0000000000d2"
		sessionID = "00000000-0000-4000-8000-0000000000d3"
		eventID   = "00000000-0000-4000-8000-0000000000d4"
	)
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.sessions (id, user_id, status) VALUES ($1::uuid, $2::uuid, 'active')`, tenant.Schema), sessionID, userID); err != nil {
		t.Fatal(err)
	}
	router := tenantdb.NewRouter(db, store)
	observations := NewObservationRepository(router)
	normalized := NewNormalizedEventRepository(router)

	event := observation.Event{
		ID: eventID, IdempotencyKey: "idem-" + eventID, TenantID: tenant.ID,
		UserID: userID, SessionID: sessionID, ConversationID: "conversation",
		SourceType: observation.SourceUser, SourceID: "user", MessageType: "message",
		Payload: json.RawMessage(`{"text":"hi"}`), OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
		Visibility: observation.VisibilityPrivate, Reliability: observation.ReliabilityHigh,
		RetentionClass: "standard", AccessLevel: observation.Level1,
	}
	stored, created, err := observations.StoreObservation(ctx, event)
	if err != nil || !created {
		t.Fatalf("StoreObservation() created=%v err=%v", created, err)
	}
	if stored.UserID != userID {
		t.Fatalf("stored owner = %q, want %q", stored.UserID, userID)
	}
	got, err := observations.GetObservation(ctx, tenant.ID, eventID)
	if err != nil || got.UserID != userID {
		t.Fatalf("GetObservation owner = %q err=%v, want %q", got.UserID, err, userID)
	}

	// An idempotent retry with the same owner returns the existing event.
	replay, created, err := observations.StoreObservation(ctx, event)
	if err != nil || created || replay.UserID != userID {
		t.Fatalf("idempotent replay created=%v owner=%q err=%v", created, replay.UserID, err)
	}
	// A retry that changes the owner is a conflict, never a silent overwrite.
	conflict := event
	conflict.UserID = otherUser
	if _, _, err := observations.StoreObservation(ctx, conflict); err == nil {
		t.Fatal("a differing owner must be an idempotency conflict")
	}

	// The owner reaches both normalized-event readers.
	normalizedID := "00000000-0000-4000-8000-0000000000d5"
	if err := normalized.StoreNormalizedEvents(ctx, tenant.ID, []ports.NormalizedEventRecord{{
		ID: normalizedID, RawEventID: eventID, SchemaVersion: "schema-v1", NormalizationVersion: "normalize-v1",
		Payload: json.RawMessage(`{"text":"hi"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	pending, err := normalized.ListPendingAnalysisEvents(ctx, tenant.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if owner := ownerOfRawEvent(pending, eventID); owner != userID {
		t.Fatalf("ListPendingAnalysisEvents owner = %q, want %q", owner, userID)
	}
	loaded, err := normalized.LoadAnalysisEventsByRawIDs(ctx, tenant.ID, []string{eventID})
	if err != nil {
		t.Fatal(err)
	}
	if owner := ownerOfRawEvent(loaded, eventID); owner != userID {
		t.Fatalf("LoadAnalysisEventsByRawIDs owner = %q, want %q", owner, userID)
	}
}

func ownerOfRawEvent(events []ports.PendingAnalysisEvent, rawEventID string) string {
	for _, event := range events {
		if event.RawEventID == rawEventID {
			return event.UserID
		}
	}
	return ""
}
