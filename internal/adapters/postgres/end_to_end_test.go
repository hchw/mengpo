package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	appobservation "github.com/hchw/mengpo/internal/application/observation"
	"github.com/hchw/mengpo/internal/application/projection"
	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestEndToEndEvidenceChain walks the pseudocode scenario end to end:
// Observe -> observed_events -> Normalize -> Project -> Failure -> Feedback, and
// verifies that every promoted candidate can be traced back to raw evidence.
func TestEndToEndEvidenceChain(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "end to end evidence chain")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	userID := "00000000-0000-4000-8000-000000000901"
	sessionID := "00000000-0000-4000-8000-000000000902"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1,$2,'active')`, sessionID, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Step 1: Observe a raw event. It must land in observed_events with a
	// durable normalization job; no memory is created here.
	observations := NewObservationRepository(router)
	jobs := NewOutboxRepository(router)
	gateway := appobservation.NewGateway(observations)
	principal := appobservation.Principal{TenantID: tenant.ID, SourceID: userID, SourceType: observation.SourceUser, AccessLevel: observation.Level0, DeploymentBound: true}
	event, created, err := gateway.IngestMessage(ctx, principal, appobservation.Input{
		SessionID: sessionID, IdempotencyKey: "e2e-event-1", MessageType: "message",
		Payload: json.RawMessage(`{"text":"migration failed because of a lock"}`), PayloadText: "migration failed because of a lock",
		Visibility: observation.VisibilitySession, Reliability: observation.ReliabilityHigh, RetentionClass: "standard",
	})
	if err != nil || !created {
		t.Fatalf("observe: created=%v err=%v", created, err)
	}
	leased, err := jobs.LeaseJobs(ctx, tenant.ID, "worker-e2e", 1, 30*time.Second)
	if err != nil || len(leased) != 1 || leased[0].JobType != "normalize_event" || leased[0].AggregateID != event.ID {
		t.Fatalf("normalize job lease=%#v err=%v", leased, err)
	}

	// Step 2: Normalize. The pipeline must bind the candidate and failure to the
	// exact raw evidence event id.
	analyst := ports.AnalystResult{
		Candidates: []ports.CandidateMemory{{CandidateID: "cand-1", EvidenceEventIDs: []string{event.ID}, ScopeType: "session", ScopeID: sessionID, Content: json.RawMessage(`{"summary":"migration lock failure"}`), Confidence: 0.7}},
		Failures:   []ports.FailureAssessment{{EventIDs: []string{event.ID}, Conclusion: "migration failed due to lock", Attribution: "direct", Confidence: 0.8}},
	}
	result, err := analysis.BuildPipelineResult(tenant.ID, "schema-v1", "normalize-v1", []analysis.RawEvent{{
		ID: event.ID, TenantID: tenant.ID, SessionID: sessionID, OccurredAt: event.OccurredAt, SourceType: string(event.SourceType), MessageType: event.MessageType, Payload: event.Payload,
	}}, analyst, time.Now().UTC())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(result.Normalized) != 1 || result.Normalized[0].RawEventID != event.ID {
		t.Fatalf("normalized event lost evidence: %#v", result.Normalized)
	}
	if len(result.Candidates) != 1 || len(result.Candidates[0].EvidenceEventIDs) != 1 || result.Candidates[0].EvidenceEventIDs[0] != event.ID {
		t.Fatalf("candidate lost evidence: %#v", result.Candidates)
	}
	if len(result.Failures) != 1 || result.Failures[0].Attribution != "direct" {
		t.Fatalf("failure assessment lost: %#v", result.Failures)
	}

	// Step 3: Persist the candidate as a candidate-status memory. It must not be
	// retrievable by default.
	memories := NewMemoryRepository(router)
	candidateNode := ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000911", IdempotencyKey: "e2e-candidate-1", UserID: userID, SessionID: sessionID,
		ScopeType: "session", ScopeID: sessionID, MemoryType: "failure", Status: "candidate", Confidence: 0.7, DefaultRetrieval: true,
		Content: json.RawMessage(`{"summary":"migration lock failure"}`), ContentText: "migration lock failure",
		Provenance: json.RawMessage(`{"evidence_event_ids":["` + event.ID + `"]}`),
	}
	stored, err := memories.Create(ctx, tenant.ID, candidateNode)
	if err != nil || stored.Status != "candidate" {
		t.Fatalf("create candidate: %#v err=%v", stored, err)
	}

	// Step 4: Project. Focus must exclude the candidate; a divergence ranking
	// that allows weak candidates can include it as an explicitly uncertain item.
	focus := recall.Rank([]ports.RecallCandidate{{Node: stored, Channels: []string{ports.RecallChannelStructured}}}, recall.RankingContext{SessionID: sessionID, IncludeCandidates: false})
	if len(focus) != 1 || focus[0].Included || focus[0].ExcludedReason != "status-candidate" {
		t.Fatalf("focus included candidate: %#v", focus)
	}
	divergence := recall.Rank([]ports.RecallCandidate{{Node: stored, Channels: []string{ports.RecallChannelStructured, ports.RecallChannelFullText}}}, recall.RankingContext{SessionID: sessionID, IncludeCandidates: true})
	if len(divergence) != 1 || !divergence[0].Included {
		t.Fatalf("divergence excluded eligible candidate: %#v", divergence)
	}
	selected, err := projection.NewBudgetManager().Select(divergence, projection.Budget{CandidateLimit: 10, RankingLimit: 10, InjectionTokenLimit: 100})
	if err != nil || len(selected.Selected) != 1 || selected.Selected[0].Candidate.Node.ID != stored.ID {
		t.Fatalf("projection selection=%#v err=%v", selected, err)
	}

	// Step 5: Feedback on the injected memory. It is durable evidence and never
	// rewrites memory content.
	feedback := NewFeedbackRepository(router)
	if err := feedback.StoreFeedback(ctx, tenant.ID, ports.MemoryFeedback{MemoryID: stored.ID, UserID: userID, SessionID: sessionID, Type: "corrected", Reason: "lock was a network timeout", RequestID: "e2e-feedback-1"}); err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if err := feedback.StoreFeedback(ctx, tenant.ID, ports.MemoryFeedback{MemoryID: stored.ID, UserID: userID, Type: "invalid", RequestID: "e2e-feedback-2"}); err == nil {
		t.Fatal("invalid feedback type accepted")
	}
	var feedbackCount int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_feedback WHERE memory_id=$1`, stored.ID).Scan(&feedbackCount)
	}); err != nil || feedbackCount != 1 {
		t.Fatalf("feedback count=%d err=%v", feedbackCount, err)
	}
	// Raw evidence must not have become a memory on its own: only the candidate
	// node exists, and it carries the evidence linkage.
	var memoryCount int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes`).Scan(&memoryCount)
	}); err != nil || memoryCount != 1 {
		t.Fatalf("memory count=%d err=%v (raw evidence must not become memory directly)", memoryCount, err)
	}
}
