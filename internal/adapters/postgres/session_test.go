package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/memoryaccess"
	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSessionScopeAuthorizationCannotCrossTenantSchemas(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := registry.NewStore(db)
	tenantA := createMigratedTenant(t, ctx, db, store, "scope isolation tenant A")
	tenantB := createMigratedTenant(t, ctx, db, store, "scope isolation tenant B")
	for _, tenant := range []auth.Tenant{tenantA, tenantB} {
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
		})
	}

	router := tenantdb.NewRouter(db, store)
	sessions := NewSessionRepository(router)
	userID := "00000000-0000-4000-8000-000000000001"
	sessionID := "00000000-0000-4000-8000-000000000011"
	if err := router.WithTenantTx(ctx, tenantA.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1, $2, 'active')`, sessionID, userID)
		return err
	}); err != nil {
		t.Fatalf("insert tenant A session: %v", err)
	}
	if ownerID, err := sessions.GetSessionOwner(ctx, tenantA.ID, sessionID); err != nil || ownerID != userID {
		t.Fatalf("tenant A session owner = %q, %v; want %q", ownerID, err, userID)
	}
	if _, err := sessions.GetSessionOwner(ctx, tenantB.ID, sessionID); !errors.Is(err, ports.ErrSessionNotFound) {
		t.Fatalf("tenant B session lookup = %v, want ErrSessionNotFound", err)
	}

	authorizer := memoryaccess.NewService(sessions)
	active := func(tenant auth.Tenant) tenants.ActiveTenantContext {
		tenant.Status = auth.TenantActive
		return tenants.ActiveTenantContext{
			UserID: userID, MembershipID: "membership-" + tenant.ID,
			Tenant: tenant,
		}
	}
	request := memoryaccess.ScopeRequest{
		TenantID: tenantA.ID, UserID: userID, ScopeType: memory.ScopeSession, SessionID: sessionID,
	}
	if _, err := authorizer.AuthorizeUser(ctx, active(tenantA), request); err != nil {
		t.Fatalf("authorize tenant A session: %v", err)
	}
	request.TenantID = tenantB.ID
	if _, err := authorizer.AuthorizeUser(ctx, active(tenantB), request); !errors.Is(err, memoryaccess.ErrForbidden) {
		t.Fatalf("authorize tenant A session through tenant B = %v, want ErrForbidden", err)
	}

	memories := NewMemoryRepository(router)
	global := ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000101", IdempotencyKey: "scope-isolation:user-global",
		UserID: userID, ScopeType: string(memory.ScopeUserGlobal), ScopeID: userID,
		MemoryType: "preference", Status: "stable", Confidence: 0.9,
		Content: []byte(`{"text":"tenant A only"}`), DefaultRetrieval: true,
	}
	if _, err := memories.Create(ctx, tenantA.ID, global); err != nil {
		t.Fatalf("create tenant A global memory: %v", err)
	}
	if _, err := memories.Get(ctx, tenantB.ID, global.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("tenant B memory lookup = %v, want ErrMemoryNotFound", err)
	}
}

func TestIdleSessionCleanupKeepsSessionsWithActivity(t *testing.T) {
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
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "session idle cleanup")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	sessions := NewSessionRepository(router)
	memories := NewMemoryRepository(router)
	userID := "00000000-0000-4000-8000-000000000301"
	oldIdle := "00000000-0000-4000-8000-000000000302"
	oldWithMemory := "00000000-0000-4000-8000-000000000303"
	recentIdle := "00000000-0000-4000-8000-000000000304"
	for i, binding := range []ports.SessionBinding{
		{TenantID: tenant.ID, SessionID: oldIdle, UserID: userID, ExternalID: "idle-old"},
		{TenantID: tenant.ID, SessionID: oldWithMemory, UserID: userID, ExternalID: "memory-old"},
		{TenantID: tenant.ID, SessionID: recentIdle, UserID: userID, ExternalID: "idle-recent"},
	} {
		if _, err := sessions.BindSession(ctx, binding); err != nil {
			t.Fatalf("bind session %d: %v", i, err)
		}
	}
	// Age two sessions well past any plausible retention window; leave the
	// third recent so it is retained regardless of activity.
	past := time.Now().UTC().Add(-30 * 24 * time.Hour)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	for _, id := range []string{oldIdle, oldWithMemory} {
		if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET started_at = $2, updated_at = $2 WHERE id = $1::uuid`, id, past)
			return err
		}); err != nil {
			t.Fatalf("age session %s: %v", id, err)
		}
	}
	node := ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000305", IdempotencyKey: "idle-cleanup:1",
		UserID: userID, SessionID: oldWithMemory, ScopeType: string(memory.ScopeSession), ScopeID: oldWithMemory,
		MemoryType: "episodic", Status: "active", Confidence: 0.8, Content: []byte(`{"text":"keep"}`),
	}
	if _, err := memories.Create(ctx, tenant.ID, node); err != nil {
		t.Fatalf("create session memory: %v", err)
	}
	idle, err := sessions.ListIdleSessions(ctx, tenant.ID, cutoff, 50)
	if err != nil {
		t.Fatalf("list idle sessions: %v", err)
	}
	if len(idle) != 1 || idle[0].ID != oldIdle {
		t.Fatalf("idle sessions = %+v, want only %s", idle, oldIdle)
	}
	if removed, err := sessions.DeleteIdleSession(ctx, tenant.ID, oldWithMemory, cutoff); err != nil || removed {
		t.Fatalf("delete session with memory = %v, %v; want kept", removed, err)
	}
	if removed, err := sessions.DeleteIdleSession(ctx, tenant.ID, recentIdle, cutoff); err != nil || removed {
		t.Fatalf("delete recent idle session = %v, %v; want kept", removed, err)
	}
	if removed, err := sessions.DeleteIdleSession(ctx, tenant.ID, oldIdle, cutoff); err != nil || !removed {
		t.Fatalf("delete idle session = %v, %v; want removed", removed, err)
	}
	if _, err := sessions.GetSessionOwner(ctx, tenant.ID, oldIdle); !errors.Is(err, ports.ErrSessionNotFound) {
		t.Fatalf("idle session still present: %v", err)
	}
	if _, err := sessions.GetSessionOwner(ctx, tenant.ID, oldWithMemory); err != nil {
		t.Fatalf("session with memory was removed: %v", err)
	}
}

func TestListSessionsWithMemoriesOnlyHidesEmptyBindings(t *testing.T) {
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
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "session memories filter")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	sessions := NewSessionRepository(router)
	memories := NewMemoryRepository(router)
	userID := "00000000-0000-4000-8000-000000000201"
	withMemory := "00000000-0000-4000-8000-000000000202"
	empty := "00000000-0000-4000-8000-000000000203"
	for _, binding := range []ports.SessionBinding{
		{TenantID: tenant.ID, SessionID: withMemory, UserID: userID, ExternalID: "ext-with"},
		{TenantID: tenant.ID, SessionID: empty, UserID: userID, ExternalID: "ext-empty"},
	} {
		if _, err := sessions.BindSession(ctx, binding); err != nil {
			t.Fatalf("bind session: %v", err)
		}
	}
	node := ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000204", IdempotencyKey: "session-memories-filter:1",
		UserID: userID, SessionID: withMemory, ScopeType: string(memory.ScopeSession), ScopeID: withMemory,
		MemoryType: "episodic", Status: "active", Confidence: 0.8,
		Content: []byte(`{"text":"remember me"}`),
	}
	if _, err := memories.Create(ctx, tenant.ID, node); err != nil {
		t.Fatalf("create session memory: %v", err)
	}
	filtered, err := sessions.ListSessions(ctx, tenant.ID, ports.SessionListRequest{UserID: userID, WithMemoriesOnly: true})
	if err != nil {
		t.Fatalf("list filtered sessions: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].ID != withMemory {
		t.Fatalf("filtered sessions = %+v, want only %s", filtered, withMemory)
	}
	all, err := sessions.ListSessions(ctx, tenant.ID, ports.SessionListRequest{UserID: userID})
	if err != nil {
		t.Fatalf("list all sessions: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("unfiltered sessions total = %d, want 2", all.Total)
	}
}

func TestBindSessionIsIdempotentAndTenantScoped(t *testing.T) {
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
	store := registry.NewStore(db)
	tenantA := createMigratedTenant(t, ctx, db, store, "session bind tenant A")
	tenantB := createMigratedTenant(t, ctx, db, store, "session bind tenant B")
	t.Cleanup(func() {
		for _, tenant := range []auth.Tenant{tenantA, tenantB} {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
		}
	})
	router := tenantdb.NewRouter(db, store)
	sessions := NewSessionRepository(router)
	userID := "00000000-0000-4000-8000-000000000101"
	agentID := "00000000-0000-4000-8000-000000000102"
	first := "00000000-0000-4000-8000-000000000103"
	if bound, err := sessions.BindSession(ctx, ports.SessionBinding{TenantID: tenantA.ID, SessionID: first, UserID: userID, AgentID: agentID, ExternalID: "ext-1", Title: "chat"}); err != nil || bound != first {
		t.Fatalf("first bind = %q, %v", bound, err)
	}
	retry := "00000000-0000-4000-8000-000000000104"
	if bound, err := sessions.BindSession(ctx, ports.SessionBinding{TenantID: tenantA.ID, SessionID: retry, UserID: userID, AgentID: agentID, ExternalID: "ext-1"}); err != nil || bound != first {
		t.Fatalf("idempotent retry = %q, %v; want %q", bound, err, first)
	}
	if owner, err := sessions.GetSessionOwner(ctx, tenantA.ID, first); err != nil || owner != userID {
		t.Fatalf("owner = %q, %v", owner, err)
	}
	if _, err := sessions.GetSessionOwner(ctx, tenantB.ID, first); !errors.Is(err, ports.ErrSessionNotFound) {
		t.Fatalf("cross-tenant session visible: %v", err)
	}
}
