package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	appobservation "github.com/hchw/mengpo/internal/application/observation"
	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRealPostgresConsistencyInvariants re-checks the 12.1/12.2/12.4 acceptance
// invariants directly against PostgreSQL instead of only domain-level inputs.
func TestRealPostgresConsistencyInvariants(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "consistency invariants")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	userID := "00000000-0000-4000-8000-00000000d001"
	sessionID := "00000000-0000-4000-8000-00000000d002"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1,$2,'active')`, sessionID, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	gateway := appobservation.NewGateway(NewObservationRepository(router))
	event, created, err := gateway.IngestMessage(ctx, appobservation.Principal{
		TenantID: tenant.ID, SourceID: userID, SourceType: obsdomain.SourceUser, AccessLevel: obsdomain.Level0, DeploymentBound: true,
	}, appobservation.Input{
		SessionID: sessionID, IdempotencyKey: "consistency-1", Payload: json.RawMessage(`{"text":"observed"}`), PayloadText: "observed",
		Visibility: obsdomain.VisibilitySession, Reliability: obsdomain.ReliabilityHigh, RetentionClass: "standard",
	})
	if err != nil || !created {
		t.Fatalf("observe created=%v err=%v", created, err)
	}

	// 12.1: raw evidence must never become memory directly.
	var memoryCount int
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes`).Scan(&memoryCount)
	}); err != nil || memoryCount != 0 {
		t.Fatalf("raw observation created %d memories, want 0 (err=%v)", memoryCount, err)
	}

	memories := NewMemoryRepository(router)
	sessionNode, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-00000000d011", IdempotencyKey: "consistency-session", UserID: userID, SessionID: sessionID,
		ScopeType: "session", ScopeID: sessionID, MemoryType: "failure", Status: "active", Confidence: 0.8, DefaultRetrieval: true,
		Content: json.RawMessage(`{"summary":"session memory"}`), ContentText: "session memory",
		Provenance: json.RawMessage(`{"evidence_event_ids":["` + event.ID + `"],"run_id":"run-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 12.2: provenance must survive the round trip and stay bound to the raw event.
	reloaded, err := memories.Get(ctx, tenant.ID, sessionNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	var provenance struct {
		EvidenceEventIDs []string `json:"evidence_event_ids"`
	}
	if err := json.Unmarshal(reloaded.Provenance, &provenance); err != nil || len(provenance.EvidenceEventIDs) != 1 || provenance.EvidenceEventIDs[0] != event.ID {
		t.Fatalf("provenance=%s err=%v", reloaded.Provenance, err)
	}

	// 12.2: a session-scope memory must not leak into the user-global channel.
	if _, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-00000000d012", IdempotencyKey: "consistency-global", UserID: userID,
		ScopeType: "user-global", ScopeID: userID, MemoryType: "fact", Status: "active", Confidence: 0.9, DefaultRetrieval: true,
		Content: json.RawMessage(`{"summary":"global memory"}`), ContentText: "global memory", Provenance: json.RawMessage(`{"evidence_event_ids":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	// 12.4: each recall channel keeps an independent, correctly scoped set.
	globalPage, err := memories.ListMemories(ctx, tenant.ID, ports.MemoryListRequest{UserID: userID, Statuses: []string{"active"}, Page: 1, PageSize: 20})
	if err != nil || len(globalPage.Items) != 1 || globalPage.Items[0].ScopeType != "user-global" {
		t.Fatalf("global channel=%#v err=%v", globalPage.Items, err)
	}
	sessionPage, err := memories.ListMemories(ctx, tenant.ID, ports.MemoryListRequest{UserID: userID, SessionID: sessionID, Statuses: []string{"active"}, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundSession := false
	for _, item := range sessionPage.Items {
		if item.ID == sessionNode.ID {
			foundSession = true
		}
	}
	if !foundSession {
		t.Fatalf("session channel omitted session memory: %#v", sessionPage.Items)
	}
}
