package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func jsonEqual(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}

func TestProjectionPersistenceAndTenantScopedCache(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenantA := createMigratedTenant(t, ctx, db, store, "projection tenant a")
	tenantB := createMigratedTenant(t, ctx, db, store, "projection tenant b")
	t.Cleanup(func() {
		for _, tenant := range []string{tenantA.ID, tenantB.ID} {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant)
		}
	})
	router := tenantdb.NewRouter(db, store)
	repository := NewProjectionRepository(router)
	userID := "00000000-0000-4000-8000-000000000041"
	sessionID := "00000000-0000-4000-8000-000000000042"
	if err := router.WithTenantTx(ctx, tenantA.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,user_id,status) VALUES($1,$2,'active')`, sessionID, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event := ports.ProjectionEvent{ID: "00000000-0000-4000-8000-000000000741", RequestID: "req-projection", UserID: userID, SessionID: sessionID, Mode: "diverge", SelectedIDs: []string{"00000000-0000-4000-8000-000000000742"}, SelectionReasons: json.RawMessage(`{"memory":{"score":0.8,"channels":["full-text"]}}`), ExcludedReasons: json.RawMessage(`{"other":{"reason":"conflict"}}`), Budget: json.RawMessage(`{"candidate":10,"tokens":100}`), Provenance: json.RawMessage(`{"memory":["full-text","relation"]}`), DegradedMode: "reranker-unavailable"}
	if err := repository.RecordProjection(ctx, tenantA.ID, event); err != nil {
		t.Fatalf("RecordProjection: %v", err)
	}
	var mode, degraded, selected string
	var selection, excluded, budget, provenance []byte
	if err := router.WithTenantTx(ctx, tenantA.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT mode,selected_memory_ids::text,selection_reasons,excluded_reasons,budget,provenance,degraded_mode FROM projection_events WHERE request_id=$1`, event.RequestID).Scan(&mode, &selected, &selection, &excluded, &budget, &provenance, &degraded)
	}); err != nil {
		t.Fatal(err)
	}
	if mode != "diverge" || degraded != "reranker-unavailable" || selected == "{}" {
		t.Fatalf("persisted mode=%s degraded=%s selected=%v", mode, degraded, selected)
	}
	for name, value := range map[string][]byte{"selection": selection, "excluded": excluded, "budget": budget, "provenance": provenance} {
		if !json.Valid(value) {
			t.Errorf("%s is invalid JSON: %s", name, value)
		}
	}

	key := "projection-cache-key"
	entry := ports.ProjectionCacheEntry{CacheKey: key, UserID: userID, SessionID: sessionID, ScopeType: "session", Response: json.RawMessage(`{"context":"private"}`), ExpiresAt: time.Now().Add(time.Minute)}
	if err := repository.PutProjectionCache(ctx, tenantA.ID, entry); err != nil {
		t.Fatalf("PutProjectionCache: %v", err)
	}
	if value, ok, err := repository.GetProjectionCache(ctx, tenantA.ID, key, userID, sessionID, "session"); err != nil || !ok || !jsonEqual(value, entry.Response) {
		t.Fatalf("cache read=%s ok=%v err=%v", value, ok, err)
	}
	if _, ok, err := repository.GetProjectionCache(ctx, tenantA.ID, key, userID, "", "user-global"); err != nil || ok {
		t.Fatalf("cache leaked across scope: ok=%v err=%v", ok, err)
	}
	if _, ok, err := repository.GetProjectionCache(ctx, tenantB.ID, key, userID, sessionID, "session"); err != nil || ok {
		t.Fatalf("cache leaked across tenant schemas: ok=%v err=%v", ok, err)
	}
	entry.CacheKey = "expired"
	entry.ExpiresAt = time.Now().Add(-time.Second)
	if err := repository.PutProjectionCache(ctx, tenantA.ID, entry); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repository.GetProjectionCache(ctx, tenantA.ID, "expired", userID, sessionID, "session"); err != nil || ok {
		t.Fatalf("expired cache returned: ok=%v err=%v", ok, err)
	}
	if err := repository.RecordProjection(ctx, tenantA.ID, ports.ProjectionEvent{RequestID: "bad", UserID: userID, Mode: "focus"}); err == nil {
		t.Fatal("projection event with missing explanations accepted")
	}
}

// TestFindProjectionResolvesOwnedReference proves a projection reference can be
// resolved to its owner and exposed memories, and that unknown, malformed, or
// cross-tenant references are reported as not-found instead of as a server
// failure or another tenant's data.
func TestFindProjectionResolvesOwnedReference(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "projection lookup")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repository := NewProjectionRepository(router)

	userID := "00000000-0000-4000-8000-000000000051"
	sessionID := "00000000-0000-4000-8000-000000000052"
	memoryID := "00000000-0000-4000-8000-000000000053"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,user_id,status) VALUES($1,$2,'active')`, sessionID, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	projectionID := "00000000-0000-4000-8000-000000000751"
	if err := repository.RecordProjection(ctx, tenant.ID, ports.ProjectionEvent{
		ID: projectionID, RequestID: "req-lookup", UserID: userID, SessionID: sessionID, Mode: "focus",
		SelectedIDs:      []string{memoryID},
		SelectionReasons: json.RawMessage(`{"memory":{"score":0.9}}`),
		ExcludedReasons:  json.RawMessage(`{"none":{"reason":"budget"}}`),
		Budget:           json.RawMessage(`{"candidate":5}`),
		Provenance:       json.RawMessage(`{"memory":["full-text"]}`),
	}); err != nil {
		t.Fatalf("RecordProjection: %v", err)
	}

	lookup, err := repository.FindProjection(ctx, tenant.ID, projectionID)
	if err != nil {
		t.Fatalf("FindProjection: %v", err)
	}
	if lookup.UserID != userID || lookup.SessionID != sessionID || len(lookup.SelectedMemoryIDs) != 1 || lookup.SelectedMemoryIDs[0] != memoryID {
		t.Fatalf("lookup = %#v", lookup)
	}

	for _, tc := range []struct{ name, id string }{
		{"unknown", "00000000-0000-4000-8000-000000000799"},
		{"malformed", "not-a-uuid"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := repository.FindProjection(ctx, tenant.ID, tc.id); !errors.Is(err, ports.ErrProjectionNotFound) {
				t.Fatalf("error = %v, want ErrProjectionNotFound", err)
			}
		})
	}

	other := createMigratedTenant(t, ctx, db, store, "projection lookup other")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+other.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, other.ID)
	})
	if _, err := repository.FindProjection(ctx, other.ID, projectionID); !errors.Is(err, ports.ErrProjectionNotFound) {
		t.Fatalf("cross-tenant error = %v, want ErrProjectionNotFound", err)
	}
}
