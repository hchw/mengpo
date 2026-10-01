package assembly

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/analysis"
	appobservation "github.com/hchw/mengpo/internal/application/observation"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestWorkerNormalizesObservedEvent drives the real normalize_event job path
// end to end: observe -> durable job -> lease -> dispatch -> normalized_events.
func TestWorkerNormalizesObservedEvent(t *testing.T) {
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
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "worker normalize")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	gateway := appobservation.NewGateway(postgres.NewObservationRepository(router))
	event, created, err := gateway.IngestMessage(ctx, appobservation.Principal{
		TenantID:        tenant.ID,
		SourceID:        testUserID,
		SourceType:      obsdomain.SourceUser,
		AccessLevel:     obsdomain.Level0,
		DeploymentBound: true,
	}, appobservation.Input{
		ConversationID: "conv-worker-1",
		IdempotencyKey: "worker-normalize-1",
		Payload:        json.RawMessage(`{"status":"error","text":"migration failed"}`),
		PayloadText:    "migration failed",
		Visibility:     obsdomain.VisibilitySession,
		Reliability:    obsdomain.ReliabilityHigh,
		RetentionClass: "standard",
	})
	if err != nil || !created {
		t.Fatalf("observe: created=%v err=%v", created, err)
	}

	outbox := postgres.NewOutboxRepository(router)
	jobs, err := outbox.LeaseJobs(ctx, tenant.ID, "worker-test", 5, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].JobType != JobNormalizeEvent || jobs[0].AggregateID != event.ID {
		t.Fatalf("leased jobs=%#v err=%v", jobs, err)
	}

	dispatcher := &JobDispatcher{
		Observations:         postgres.NewObservationRepository(router),
		Normalized:           postgres.NewNormalizedEventRepository(router),
		Analysis:             analysis.New(analysis.Providers{Analyst: analysis.RuleFallback{}}),
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		NewID:                newUUID,
	}
	if err := dispatcher.Handle(ctx, jobs[0]); err != nil {
		t.Fatalf("dispatch normalize_event: %v", err)
	}

	var count int
	var status string
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM normalized_events WHERE raw_event_id=$1::uuid`, event.ID).Scan(&count); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT processing_status FROM normalized_events WHERE raw_event_id=$1::uuid`, event.ID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "processed" {
		t.Fatalf("normalized events=%d status=%s", count, status)
	}

	// Re-dispatching the same job must be idempotent.
	if err := dispatcher.Handle(ctx, jobs[0]); err != nil {
		t.Fatalf("re-dispatch normalize_event: %v", err)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM normalized_events`).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("normalized events after re-dispatch = %d err=%v", count, err)
	}
}

// TestJobDispatcherRejectsUnknownType keeps unroutable work on the durable
// retry/dead-letter path instead of dropping it.
func TestJobDispatcherRejectsUnknownType(t *testing.T) {
	dispatcher := &JobDispatcher{}
	err := dispatcher.Handle(context.Background(), ports.OutboxJob{JobType: "mystery"})
	if !errors.Is(err, ErrUnsupportedJobType) {
		t.Fatalf("Handle(unknown) error = %v, want ErrUnsupportedJobType", err)
	}
	if err := dispatcher.Handle(context.Background(), ports.OutboxJob{JobType: JobConsolidate}); err == nil {
		t.Fatal("unconfigured consolidate must fail so the durable path retries instead of silently dropping work")
	}
}
