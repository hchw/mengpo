package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestObservationOutboxIsAtomicAndLeasesRecover(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "outbox lease integration")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	observations := NewObservationRepository(router)
	jobs := NewOutboxRepository(router)
	event := observation.Event{ID: "00000000-0000-4000-8000-000000000021", TenantID: tenant.ID, IdempotencyKey: "integration-event-key",
		SourceEventID: "src-1", SourceType: observation.SourceTool, SourceID: "tool-1", MessageType: "tool.result",
		Payload: json.RawMessage(`{"ok":true}`), OccurredAt: time.Now().UTC(), Visibility: observation.VisibilitySession,
		Reliability: observation.ReliabilityHigh, RetentionClass: "test"}
	stored, created, err := observations.StoreObservation(ctx, event)
	if err != nil || !created {
		t.Fatalf("StoreObservation() created=%v err=%v", created, err)
	}
	duplicate, created, err := observations.StoreObservation(ctx, event)
	if err != nil || created || duplicate.ID != stored.ID {
		t.Fatalf("duplicate StoreObservation()=%v,%v,%v", duplicate.ID, created, err)
	}

	leased, err := jobs.LeaseJobs(ctx, tenant.ID, "worker-a", 1, 30*time.Second)
	if err != nil || len(leased) != 1 || leased[0].ID != event.ID || leased[0].JobType != "normalize_event" {
		t.Fatalf("lease jobs=%#v err=%v", leased, err)
	}
	// Simulate a crashed worker: expire the lease, then confirm another worker recovers it.
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE outbox_jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, event.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := jobs.LeaseJobs(ctx, tenant.ID, "worker-b", 1, 30*time.Second)
	if err != nil || len(recovered) != 1 || recovered[0].ID != event.ID || recovered[0].Attempts != 2 {
		t.Fatalf("recovered jobs=%#v err=%v", recovered, err)
	}
	if err := jobs.CompleteJob(ctx, tenant.ID, event.ID, "worker-a"); err != ErrJobLeaseLost {
		t.Fatalf("stale completion error=%v", err)
	}
	if err := jobs.CompleteJob(ctx, tenant.ID, event.ID, "worker-b"); err != nil {
		t.Fatalf("complete recovered job: %v", err)
	}
	if err := jobs.ReplayDeadLetter(ctx, tenant.ID, event.ID); err == nil {
		t.Fatal("completed job must not be replayable")
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE outbox_jobs SET status='dead_letter', attempts=max_attempts WHERE id=$1`, event.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.ReplayDeadLetter(ctx, tenant.ID, event.ID); err != nil {
		t.Fatalf("replay dead-letter: %v", err)
	}
	replayed, err := jobs.LeaseJobs(ctx, tenant.ID, "worker-c", 1, 30*time.Second)
	if err != nil || len(replayed) != 1 || replayed[0].ID != event.ID || replayed[0].Attempts != 1 {
		t.Fatalf("replayed lease=%#v err=%v", replayed, err)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE outbox_jobs SET max_attempts=attempts WHERE id=$1`, event.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.RetryJob(ctx, tenant.ID, event.ID, "worker-c", "permanent", time.Now().Add(time.Second)); err != nil {
		t.Fatalf("retry exhausted job: %v", err)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM outbox_jobs WHERE id=$1`, event.ID).Scan(&status); err != nil {
			return err
		}
		if status != "dead_letter" {
			t.Fatalf("status=%s, want dead_letter", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cancelID := "00000000-0000-4000-8000-000000000033"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO outbox_jobs(id,job_type,tenant_id,idempotency_key) VALUES($1,'cancel_test',$2,'cancel-test')`, cancelID, tenant.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.CancelJob(ctx, tenant.ID, cancelID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	cancelled, err := jobs.LeaseJobs(ctx, tenant.ID, "worker-d", 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range cancelled {
		if job.ID == cancelID {
			t.Fatal("cancelled job was leased")
		}
	}

	// A job-key collision must roll back the raw observation insert in the same transaction.
	rollbackEvent := event
	rollbackEvent.ID = "00000000-0000-4000-8000-000000000022"
	rollbackEvent.IdempotencyKey = "rollback-event-key"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO outbox_jobs(id,job_type,tenant_id,idempotency_key) VALUES ($1,'test',$2,$3)`, "00000000-0000-4000-8000-000000000099", tenant.ID, "normalize:"+rollbackEvent.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := observations.StoreObservation(ctx, rollbackEvent); err == nil {
		t.Fatal("expected outbox conflict")
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM observed_events WHERE id=$1)`, rollbackEvent.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			t.Fatal("observation was not rolled back with failed outbox insert")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
