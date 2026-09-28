package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	governanceapp "github.com/hchw/mengpo/internal/application/governance"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

func TestApplyGovernanceMutationDeletesDerivedDataAndAuditsAtomically(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "governance deletion tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repository := NewMemoryRepository(router)
	const (
		userID       = "00000000-0000-4000-8000-000000000401"
		memoryID     = "00000000-0000-4000-8000-000000000402"
		relatedID    = "00000000-0000-4000-8000-000000000403"
		eventID      = "00000000-0000-4000-8000-000000000404"
		evidenceID   = "00000000-0000-4000-8000-000000000405"
		relationID   = "00000000-0000-4000-8000-000000000406"
		feedbackID   = "00000000-0000-4000-8000-000000000407"
		jobID        = "00000000-0000-4000-8000-000000000408"
		analysisID   = "00000000-0000-4000-8000-000000000409"
		cacheID      = "delete-cache-key"
		retrievalID  = "00000000-0000-4000-8000-000000000410"
		projectID    = "00000000-0000-4000-8000-000000000411"
		auditID      = "00000000-0000-4000-8000-000000000412"
		payloadJobID = "00000000-0000-4000-8000-000000000414"
	)
	original := ports.MemoryNodeRecord{
		ID: memoryID, IdempotencyKey: "memory-delete-1", UserID: userID,
		ScopeType: "user-global", ScopeID: userID, MemoryType: "preference",
		Status: "stable", Visibility: "private", Confidence: 0.95,
		Applicability: json.RawMessage(`{"conditions":["safe"]}`),
		Content:       json.RawMessage(`{"text":"secret original preference"}`), ContentText: "secret original preference",
		DefaultRetrieval: true, Provenance: json.RawMessage(`{"source_event_ids":["` + eventID + `"],"reason":"secret provenance"}`),
	}
	created, err := repository.Create(ctx, tenant.ID, original)
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	related := original
	related.ID = relatedID
	related.IdempotencyKey = "memory-related-1"
	related.Content = json.RawMessage(`{"text":"related"}`)
	related.ContentText = "related"
	if _, err := repository.Create(ctx, tenant.ID, related); err != nil {
		t.Fatalf("create related memory: %v", err)
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO observed_events (id, idempotency_key, source_type, source_id, message_type, payload, occurred_at, retention_class) VALUES ($1, 'event-delete-1', 'user', 'console', 'message', '{"text":"source remains"}', now(), 'standard')`, eventID); err != nil {
			return err
		}
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO memory_evidence (id, memory_id, raw_event_id, evidence_role, confidence, attribution) VALUES ($1, $2, $3, 'supports', 0.9, 'direct')`, []any{evidenceID, memoryID, eventID}},
			{`INSERT INTO memory_relations (id, source_memory_id, target_memory_id, relation_type) VALUES ($1, $2, $3, 'related')`, []any{relationID, memoryID, relatedID}},
			{`INSERT INTO memory_feedback (id, memory_id, user_id, feedback_type, request_id) VALUES ($1, $2, $3, 'helpful', 'feedback-request')`, []any{feedbackID, memoryID, userID}},
			{`INSERT INTO embedding_jobs (id, memory_id, model_id, artifact, model_version, idempotency_key) VALUES ($1, $2, 'model', 'artifact', 'v1', 'embed-delete-1')`, []any{analysisID, memoryID}},
			{`INSERT INTO retrieval_cache (cache_key, user_id, scope_type, response, expires_at) VALUES ($1, $2, 'user-global', '{"text":"cached private content"}', now() + interval '1 hour')`, []any{cacheID, userID}},
			{`INSERT INTO retrieval_events (id, user_id, request_id, query_hash, mode, selected_memory_ids) VALUES ($1, $2, 'retrieval-request', 'hash', 'focus', ARRAY[$3::uuid])`, []any{retrievalID, userID, memoryID}},
			{`INSERT INTO projection_events (id, request_id, user_id, mode, selected_memory_ids, provenance) VALUES ($1, 'projection-request', $2, 'focus', ARRAY[$3::uuid], '{"secret":"old memory"}')`, []any{projectID, userID, memoryID}},
			{`INSERT INTO outbox_jobs (id, job_type, tenant_id, aggregate_id, idempotency_key, payload) VALUES ($1, 'analyze', $2, $3, 'delete-job-key', '{"secret":"old memory"}')`, []any{jobID, tenant.ID, memoryID}},
			{`INSERT INTO outbox_jobs (id, job_type, tenant_id, idempotency_key, payload) VALUES ($1, 'analyze', $2, 'delete-payload-job-key', jsonb_build_object('memory_id', $3::text))`, []any{payloadJobID, tenant.ID, memoryID}},
			{`INSERT INTO analysis_jobs (id, outbox_job_id, task_type, provider, model, prompt_version, schema_version, result) VALUES ($1, $2, 'analysis', 'provider', 'model', 'p1', 's1', '{"secret":"old memory"}')`, []any{analysisID, jobID}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE memory_nodes SET embedding_status = 'ready' WHERE id = $1`, memoryID)
		return err
	}); err != nil {
		t.Fatalf("insert derived data: %v", err)
	}

	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	service := governanceapp.NewService(repository)
	command := memory.GovernanceCommand{
		Action: memory.GovernanceDelete, ExpectedVersion: created.Version, AuditID: auditID,
		ActorType: "user", ActorID: userID, RequestID: "delete-request", At: now,
	}
	mutation, err := service.Apply(ctx, tenant.ID, memoryID, command)
	if err != nil {
		t.Fatalf("apply delete mutation: %v", err)
	}
	mutationRecord := governanceapp.ToRecord(mutation, created.Version)
	storedMemory, err := repository.Get(ctx, tenant.ID, memoryID)
	if err != nil {
		t.Fatalf("load deleted memory: %v", err)
	}
	result := ports.MemoryGovernanceMutationResult{Memory: storedMemory}
	if result.Memory.Version != 2 || result.Memory.DeletedAt == nil || result.Memory.ContentText != "" || string(result.Memory.Content) != `{}` {
		t.Fatalf("deleted memory record = %#v", result.Memory)
	}

	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		checks := []struct {
			name  string
			query string
			args  []any
			want  int
		}{
			{"evidence", `SELECT count(*) FROM memory_evidence WHERE memory_id = $1`, []any{memoryID}, 0},
			{"relations", `SELECT count(*) FROM memory_relations WHERE source_memory_id = $1 OR target_memory_id = $1`, []any{memoryID}, 0},
			{"feedback", `SELECT count(*) FROM memory_feedback WHERE memory_id = $1`, []any{memoryID}, 0},
			{"embedding jobs", `SELECT count(*) FROM embedding_jobs WHERE memory_id = $1`, []any{memoryID}, 0},
			{"retrieval cache", `SELECT count(*) FROM retrieval_cache WHERE user_id = $1`, []any{userID}, 0},
			{"retrieval trace", `SELECT count(*) FROM retrieval_events WHERE $1::uuid = ANY(selected_memory_ids)`, []any{memoryID}, 0},
			{"projection trace", `SELECT count(*) FROM projection_events WHERE $1::uuid = ANY(selected_memory_ids)`, []any{memoryID}, 0},
			{"raw event retained", `SELECT count(*) FROM observed_events WHERE id = $1`, []any{eventID}, 1},
			{"cancelled and cleared outbox", `SELECT count(*) FROM outbox_jobs WHERE id = $1 AND status = 'cancelled' AND payload = '{}'::jsonb AND lease_until IS NULL`, []any{jobID}, 1},
			{"cancelled payload-linked outbox", `SELECT count(*) FROM outbox_jobs WHERE id = $1 AND status = 'cancelled' AND payload = '{}'::jsonb`, []any{payloadJobID}, 1},
			{"cancelled and cleared analysis result", `SELECT count(*) FROM analysis_jobs WHERE id = $1 AND status = 'cancelled' AND result IS NULL`, []any{analysisID}, 1},
			{"audit row", `SELECT count(*) FROM audit_events WHERE id = $1 AND action = 'delete' AND resource_id = $2`, []any{auditID, memoryID}, 1},
		}
		for _, check := range checks {
			var count int
			if err := tx.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
				return err
			}
			if count != check.want {
				t.Errorf("%s rows = %d, want %d", check.name, count, check.want)
			}
		}
		var auditChanges string
		if err := tx.QueryRowContext(ctx, `SELECT changes::text FROM audit_events WHERE id = $1`, auditID).Scan(&auditChanges); err != nil {
			return err
		}
		if containsAny(auditChanges, "secret original preference", "secret provenance", "old memory") {
			t.Errorf("audit stored deleted content: %s", auditChanges)
		}
		var deletedAt sql.NullTime
		var storedContent, storedText string
		var embeddingStatus string
		if err := tx.QueryRowContext(ctx, `SELECT deleted_at, content::text, content_text, embedding_status FROM memory_nodes WHERE id = $1`, memoryID).Scan(&deletedAt, &storedContent, &storedText, &embeddingStatus); err != nil {
			return err
		}
		if !deletedAt.Valid || storedContent != `{}` || storedText != "" || embeddingStatus != "stale" {
			t.Errorf("stored tombstone deleted_at=%v content=%q text=%q embedding=%s", deletedAt, storedContent, storedText, embeddingStatus)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify deletion effects: %v", err)
	}

	// An exact retry with the same audit ID returns the committed result without
	// replaying the terminal transition or duplicating the audit event.
	replayed, err := service.Apply(ctx, tenant.ID, memoryID, command)
	if err != nil {
		t.Fatalf("idempotent governance retry: %v", err)
	}
	if replayed.Memory.Version != 2 || replayed.Memory.DeletedAt == nil {
		t.Fatalf("idempotent retry result = %#v", replayed.Memory)
	}
	wrongVersionRetry := command
	wrongVersionRetry.ExpectedVersion--
	if _, err := service.Apply(ctx, tenant.ID, memoryID, wrongVersionRetry); !errors.Is(err, memory.ErrGovernanceVersionConflict) {
		t.Fatalf("retry with mismatched expected version = %v, want ErrGovernanceVersionConflict", err)
	}
	staleRecord := mutationRecord
	staleRecord.Audit.ID = "00000000-0000-4000-8000-000000000413"
	if _, err := repository.ApplyGovernanceMutation(ctx, tenant.ID, staleRecord); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale mutation error = %v, want ErrVersionConflict", err)
	}
}

func TestApplyGovernanceMutationPersistsCorrectionAndSupersedesRelation(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "governance correction tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	repository := NewMemoryRepository(tenantdb.NewRouter(db, store))
	currentRecord, err := repository.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000421", IdempotencyKey: "correct-old", UserID: "00000000-0000-4000-8000-000000000420",
		ScopeType: "user-global", ScopeID: "00000000-0000-4000-8000-000000000420", MemoryType: "preference",
		Status: "stable", Confidence: 0.9, Applicability: json.RawMessage(`{}`), Content: json.RawMessage(`{"text":"old value"}`),
		ContentText: "old value", DefaultRetrieval: true, Provenance: json.RawMessage(`{"source_event_ids":["event-before"]}`),
	})
	if err != nil {
		t.Fatalf("create original memory: %v", err)
	}
	current := memory.Memory{
		ID: currentRecord.ID, IdempotencyKey: currentRecord.IdempotencyKey, UserID: currentRecord.UserID,
		ScopeType: memory.ScopeType(currentRecord.ScopeType), ScopeID: currentRecord.ScopeID,
		Type: currentRecord.MemoryType, Status: memory.StatusStable, Visibility: memory.Visibility(currentRecord.Visibility),
		Confidence: currentRecord.Confidence, Applicability: memory.Applicability{}, Content: currentRecord.Content,
		ContentText: currentRecord.ContentText, DefaultRetrieval: true, Version: currentRecord.Version,
		CreatedAt: currentRecord.CreatedAt, UpdatedAt: currentRecord.UpdatedAt,
		Provenance: memory.Provenance{SourceEventIDs: []string{"event-before"}},
	}
	replacement := current
	replacement.ID = "00000000-0000-4000-8000-000000000422"
	replacement.IdempotencyKey = "correct-new"
	replacement.Status = memory.StatusCandidate
	replacement.Confidence = 0.6
	replacement.Content = json.RawMessage(`{"text":"corrected value"}`)
	replacement.ContentText = "corrected value"
	replacement.Provenance = memory.Provenance{SourceEventIDs: []string{"event-correction"}}
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	service := governanceapp.NewService(repository)
	_, err = service.Apply(ctx, tenant.ID, currentRecord.ID, memory.GovernanceCommand{
		Action: memory.GovernanceCorrect, ExpectedVersion: current.Version,
		AuditID: "00000000-0000-4000-8000-000000000423", ActorType: "user", ActorID: current.UserID,
		RequestID: "correct-request", RelationID: "00000000-0000-4000-8000-000000000424", At: now, Replacement: &replacement,
	})
	if err != nil {
		t.Fatalf("apply correction mutation: %v", err)
	}
	storedOriginal, err := repository.Get(ctx, tenant.ID, currentRecord.ID)
	if err != nil {
		t.Fatalf("load superseded memory: %v", err)
	}
	storedReplacement, err := repository.Get(ctx, tenant.ID, replacement.ID)
	if err != nil {
		t.Fatalf("load corrected memory: %v", err)
	}
	result := ports.MemoryGovernanceMutationResult{Memory: storedOriginal, Replacement: &storedReplacement}
	if result.Memory.Status != "expired" || result.Memory.DefaultRetrieval || result.Memory.Version != 2 || result.Replacement == nil ||
		result.Replacement.Status != "stable" || result.Replacement.Confidence != 1 || !result.Replacement.DefaultRetrieval || result.Replacement.Version != 1 {
		t.Fatalf("persisted correction result = %#v", result)
	}
	if err := NewMemoryRepository(tenantdb.NewRouter(db, store)).router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var relationType string
		if err := tx.QueryRowContext(ctx, `SELECT relation_type FROM memory_relations WHERE source_memory_id = $1 AND target_memory_id = $2`, result.Replacement.ID, result.Memory.ID).Scan(&relationType); err != nil {
			return err
		}
		if relationType != "supersedes" {
			t.Errorf("relation type = %q, want supersedes", relationType)
		}
		var changes []byte
		if err := tx.QueryRowContext(ctx, `SELECT changes FROM audit_events WHERE id = $1`, "00000000-0000-4000-8000-000000000423").Scan(&changes); err != nil {
			return err
		}
		var auditChanges memory.GovernanceAuditChanges
		if err := json.Unmarshal(changes, &auditChanges); err != nil {
			return err
		}
		if auditChanges.RelatedResourceID != result.Replacement.ID || auditChanges.RelatedResourceVersion != 1 || auditChanges.BeforeVersion != 1 || auditChanges.AfterVersion != 2 {
			t.Errorf("correction audit changes = %#v", auditChanges)
		}
		_, err := tx.ExecContext(ctx, `UPDATE memory_nodes SET embedding_status = 'ready' WHERE id = $1`, result.Replacement.ID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_jobs (id, memory_id, model_id, artifact, model_version, idempotency_key) VALUES ('00000000-0000-4000-8000-000000000425', $1, 'model', 'artifact', 'v1', 'redact-embedding-job')`, result.Replacement.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO retrieval_cache (cache_key, user_id, scope_type, response, expires_at) VALUES ('redact-cache-key', $1, 'user-global', '{"text":"corrected value"}', now() + interval '1 hour')`, current.UserID)
		return err
	}); err != nil {
		t.Fatalf("verify correction and prepare redaction: %v", err)
	}

	_, err = service.Apply(ctx, tenant.ID, result.Replacement.ID, memory.GovernanceCommand{
		Action: memory.GovernanceRedact, ExpectedVersion: result.Replacement.Version,
		AuditID: "00000000-0000-4000-8000-000000000426", ActorType: "user", ActorID: current.UserID,
		RequestID: "redact-request", At: now.Add(time.Minute),
		Redaction: &memory.MemoryRedaction{Content: json.RawMessage(`{"text":"[redacted]"}`), ContentText: "[redacted]"},
	})
	if err != nil {
		t.Fatalf("apply redaction mutation: %v", err)
	}
	if err := repository.router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		var content, contentText, embeddingStatus, auditChanges string
		var cacheCount, activeJobs, relationCount int
		if err := tx.QueryRowContext(ctx, `SELECT content::text, content_text, embedding_status FROM memory_nodes WHERE id = $1`, result.Replacement.ID).Scan(&content, &contentText, &embeddingStatus); err != nil {
			return err
		}
		if content != `{"text": "[redacted]"}` && content != `{"text":"[redacted]"}` || contentText != "[redacted]" || embeddingStatus != "stale" {
			t.Errorf("redacted content=%q text=%q embedding=%q", content, contentText, embeddingStatus)
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM retrieval_cache WHERE user_id = $1`, current.UserID).Scan(&cacheCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM embedding_jobs WHERE memory_id = $1 AND status = 'queued'`, result.Replacement.ID).Scan(&activeJobs); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_relations WHERE source_memory_id = $1 AND target_memory_id = $2`, result.Replacement.ID, result.Memory.ID).Scan(&relationCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT changes::text FROM audit_events WHERE id = $1`, "00000000-0000-4000-8000-000000000426").Scan(&auditChanges); err != nil {
			return err
		}
		if cacheCount != 0 || activeJobs != 0 || relationCount != 1 || !strings.Contains(auditChanges, `"redacted": true`) {
			t.Errorf("redaction side effects cache=%d jobs=%d relation=%d audit=%s", cacheCount, activeJobs, relationCount, auditChanges)
		}
		if containsAny(auditChanges, "corrected value", "[redacted]") {
			t.Errorf("redaction audit contains content: %s", auditChanges)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify redaction effects: %v", err)
	}
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if fragment != "" && strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
