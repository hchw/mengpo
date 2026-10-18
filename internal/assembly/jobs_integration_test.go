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
	"github.com/hchw/mengpo/internal/application/governance"
	appobservation "github.com/hchw/mengpo/internal/application/observation"
	"github.com/hchw/mengpo/internal/application/recall"
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

// TestConsolidateJobReadsNormalizedEvents drives the periodic consolidation
// reader end to end. Before the Scan fix, LoadAnalysisEventsByRawIDs returned
// "expected 7 destination arguments in Scan, not 6", so every consolidate job
// dead-lettered instead of producing session memory.
func TestConsolidateJobReadsNormalizedEvents(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "worker consolidate")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	const userID = "00000000-0000-4000-8000-0000000000c1"
	const sessionID = "00000000-0000-4000-8000-0000000000c2"
	const rawEventID = "00000000-0000-4000-8000-0000000000c3"
	const normalizedID = "00000000-0000-4000-8000-0000000000c4"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status, title) VALUES ($1::uuid, $2::uuid, 'active', 'consolidate')`, sessionID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO observed_events (id, idempotency_key, session_id, source_type, source_id, message_type, payload, occurred_at, retention_class, attribution_level)
VALUES ($1::uuid, 'consolidate-event', $2::uuid, 'workflow', 'src', 'context.compaction', '{"summary":"distilled span"}'::jsonb, now(), 'standard', 'correlated')`, rawEventID, sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO normalized_events (id, raw_event_id, schema_version, normalized_payload, normalization_version, sequence, processing_status)
VALUES ($1::uuid, $2::uuid, 'v1', '{"data":{"summary":"distilled span"}}'::jsonb, 'v1', 1, 'normalized')`, normalizedID, rawEventID)
		return err
	}); err != nil {
		t.Fatalf("seed consolidate fixtures: %v", err)
	}

	dispatcher := &JobDispatcher{
		AnalysisReader: postgres.NewNormalizedEventRepository(router),
		Analysis:       analysis.New(analysis.Providers{Analyst: analysis.RuleFallback{}}),
		SchemaVersion:  "schema-v1",
		NewID:          newUUID,
		Runs:           postgres.NewAnalystRunRepository(router),
	}
	job := ports.OutboxJob{
		ID:       "00000000-0000-4000-8000-0000000000c5",
		TenantID: tenant.ID,
		JobType:  JobConsolidate,
		Payload:  json.RawMessage(`{"raw_event_ids":["` + rawEventID + `"]}`),
	}
	if err := dispatcher.Handle(ctx, job); err != nil {
		t.Fatalf("consolidate must read normalized events without error, got %v", err)
	}
}

// TestSessionlessExplicitRememberBecomesRecallable drives the full path for a
// remember that carries no session: the observation's own owner scopes the
// memory, it is activated, and it is recallable without a session id.
func TestSessionlessExplicitRememberBecomesRecallable(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "sessionless remember")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)

	const userID = "00000000-0000-4000-8000-0000000000f1"
	gateway := appobservation.NewGateway(postgres.NewObservationRepository(router))
	event, created, err := gateway.IngestMessage(ctx, appobservation.Principal{
		TenantID:        tenant.ID,
		SourceID:        userID,
		SourceType:      obsdomain.SourceUser,
		AccessLevel:     obsdomain.Level0,
		DeploymentBound: true,
	}, appobservation.Input{
		ConversationID: "conv-sessionless",
		UserID:         userID,
		MessageType:    "user_remember",
		IdempotencyKey: "sessionless-remember-1",
		Payload:        json.RawMessage(`{"remember":true,"text":"always set GOPROXY"}`),
		PayloadText:    "always set GOPROXY",
		Visibility:     obsdomain.VisibilitySession,
		Reliability:    obsdomain.ReliabilityHigh,
		RetentionClass: "standard",
	})
	if err != nil || !created {
		t.Fatalf("observe: created=%v err=%v", created, err)
	}
	if event.UserID != userID {
		t.Fatalf("stored observation owner = %q, want %q", event.UserID, userID)
	}

	outbox := postgres.NewOutboxRepository(router)
	jobs, err := outbox.LeaseJobs(ctx, tenant.ID, "worker-test", 5, time.Minute)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("leased jobs=%#v err=%v", jobs, err)
	}
	dispatcher := &JobDispatcher{
		Observations:                 postgres.NewObservationRepository(router),
		Normalized:                   postgres.NewNormalizedEventRepository(router),
		Candidates:                   postgres.NewCandidateRepository(router),
		Analysis:                     analysis.New(analysis.Providers{Analyst: analysis.RuleFallback{}}),
		Governance:                   governance.NewService(postgres.NewMemoryRepository(router)),
		ExplicitRememberAutoActivate: true,
		SchemaVersion:                "schema-v1",
		NormalizationVersion:         "normalize-v1",
		NewID:                        newUUID,
	}
	for _, job := range jobs {
		if err := dispatcher.Handle(ctx, job); err != nil {
			t.Fatalf("dispatch %s: %v", job.JobType, err)
		}
	}

	var status, scopeType, owner string
	var retrievable bool
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT status, scope_type, user_id::text, default_retrieval FROM memory_nodes WHERE user_id=$1::uuid AND scope_type='user-global' ORDER BY created_at DESC LIMIT 1`, userID).Scan(&status, &scopeType, &owner, &retrievable)
	}); err != nil {
		t.Fatalf("sessionless remember was not persisted: %v", err)
	}
	if status != "active" || scopeType != "user-global" || owner != userID || !retrievable {
		t.Fatalf("remembered node = status:%s scope:%s owner:%s retrievable:%v", status, scopeType, owner, retrievable)
	}

	service := recall.NewService(
		postgres.NewSearchRepository(router),
		postgres.NewRelationRepository(router),
		postgres.NewMemoryRepository(router),
		postgres.NewEmbeddingRepository(router),
	)
	candidates, _, err := service.RecallWithMeta(ctx, tenant.ID, userID, "", recall.RecallRequest{Query: "GOPROXY"})
	if err != nil {
		t.Fatalf("RecallWithMeta() error = %v", err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.Node.UserID == userID && candidate.Node.ScopeType == "user-global" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sessionless memory not recalled without a session; got %d candidates", len(candidates))
	}
}

// TestSessionBoundarySummaryScopeRequiresSession pins the boundary: a session
// boundary yields session working memory only when a session is present, while a
// sessionless observation can only yield user-global memory.
func TestSessionBoundarySummaryScopeRequiresSession(t *testing.T) {
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

	const (
		userID    = "00000000-0000-4000-8000-0000000000f2"
		sessionID = "00000000-0000-4000-8000-0000000000f3"
	)
	for _, tc := range []struct {
		name       string
		sessionID  string
		wantMemory bool
	}{
		{"with session", sessionID, true},
		{"without session", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenant := createMigratedTenant(t, ctx, db, store, "boundary "+tc.name)
			t.Cleanup(func() {
				_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
				_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
			})
			router := tenantdb.NewRouter(db, store)
			if tc.sessionID != "" {
				if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1::uuid, $2::uuid, 'active')`, tc.sessionID, userID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			gateway := appobservation.NewGateway(postgres.NewObservationRepository(router))
			if _, _, err := gateway.IngestWorkflow(ctx, appobservation.Principal{
				TenantID: tenant.ID, SourceID: userID, AccessLevel: obsdomain.Level0, DeploymentBound: true,
			}, appobservation.Input{
				SessionID: tc.sessionID, ConversationID: "conv-boundary", UserID: userID, MessageType: "context.compaction",
				IdempotencyKey: "boundary-" + tc.name,
				Payload:        json.RawMessage(`{"context_summary":true,"summary":"the build needs GOPROXY"}`),
				PayloadText:    "the build needs GOPROXY", Visibility: obsdomain.VisibilitySession,
				Reliability: obsdomain.ReliabilityHigh, RetentionClass: "standard",
			}); err != nil {
				t.Fatalf("observe boundary: %v", err)
			}

			outbox := postgres.NewOutboxRepository(router)
			jobs, err := outbox.LeaseJobs(ctx, tenant.ID, "worker-test", 5, time.Minute)
			if err != nil || len(jobs) == 0 {
				t.Fatalf("leased jobs=%#v err=%v", jobs, err)
			}
			dispatcher := &JobDispatcher{
				Observations:         postgres.NewObservationRepository(router),
				Normalized:           postgres.NewNormalizedEventRepository(router),
				WorkingMemory:        postgres.NewWorkingMemoryRepository(router),
				Candidates:           postgres.NewCandidateRepository(router),
				SessionOwner:         postgres.NewSessionRepository(router),
				Analysis:             analysis.New(analysis.Providers{Analyst: analysis.RuleFallback{}}),
				SchemaVersion:        "schema-v1",
				NormalizationVersion: "normalize-v1",
				NewID:                newUUID,
			}
			for _, job := range jobs {
				if err := dispatcher.Handle(ctx, job); err != nil {
					t.Fatalf("dispatch %s: %v", job.JobType, err)
				}
			}

			var count int
			if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes WHERE scope_type='session'`).Scan(&count)
			}); err != nil {
				t.Fatal(err)
			}
			if tc.wantMemory && count != 1 {
				t.Fatalf("session boundary produced %d session memories, want 1", count)
			}
			if !tc.wantMemory && count != 0 {
				t.Fatalf("sessionless boundary produced %d session memories, want 0", count)
			}
		})
	}
}
