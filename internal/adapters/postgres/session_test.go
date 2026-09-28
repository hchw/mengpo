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
