package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	idUserID       = "11111111-1111-4111-8111-111111111111"
	idTenantID     = "22222222-2222-4222-8222-222222222222"
	idMembershipID = "33333333-3333-4333-8333-333333333333"
	idRoleID       = "44444444-4444-4444-8444-444444444444"
	idSessionID    = "55555555-5555-4555-8555-555555555555"
	idAgentID      = "66666666-6666-4666-8666-666666666666"
)

// TestIdentityAndAgentPersistence exercises users, memberships, auth sessions
// and agent credentials against a real PostgreSQL schema.
func TestIdentityAndAgentPersistence(t *testing.T) {
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
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1::uuid`, idTenantID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.users WHERE id=$1::uuid`, idUserID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.permissions WHERE code='agent.manage'`)
	})

	if _, err := db.ExecContext(ctx, `INSERT INTO public.permissions (code, description) VALUES ('agent.manage','manage agents') ON CONFLICT (code) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.tenants (id, name, status) VALUES ($1::uuid,'identity test','active')`, idTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.users (id, email, display_name, password_hash, status) VALUES ($1::uuid,'owner@example.com','Owner','','active')`, idUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.roles (id, tenant_id, name) VALUES ($1::uuid,$2::uuid,'owner')`, idRoleID, idTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.role_permissions (role_id, permission_code) VALUES ($1::uuid,'agent.manage')`, idRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.tenant_memberships (id, user_id, tenant_id, status) VALUES ($1::uuid,$2::uuid,$3::uuid,'active')`, idMembershipID, idUserID, idTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.membership_roles (membership_id, role_id) VALUES ($1::uuid,$2::uuid)`, idMembershipID, idRoleID); err != nil {
		t.Fatal(err)
	}

	repo := NewIdentityRepository(db)
	user, err := repo.UpsertUserByEmail(ctx, auth.User{ID: idUserID, Email: "owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	if user.ID != idUserID {
		t.Fatalf("unexpected user: %#v", user)
	}

	// tenants.Service resolves a membership context from the repository.
	tenantService := tenants.NewService(repo)
	options, err := tenantService.ListTenants(ctx, idUserID)
	if err != nil || len(options) != 1 || options[0].Tenant.ID != idTenantID {
		t.Fatalf("ListTenants=%#v err=%v", options, err)
	}
	active, err := tenantService.SelectTenant(ctx, idUserID, idTenantID)
	if err != nil {
		t.Fatalf("SelectTenant: %v", err)
	}
	if len(active.Permissions) != 1 || active.Permissions[0] != "agent.manage" {
		t.Fatalf("unexpected permissions: %#v", active.Permissions)
	}

	// Sessions only store a token hash and resolve while unexpired.
	tokenHash := sha256.Sum256([]byte("session-token"))
	if err := repo.CreateSession(ctx, auth.AuthSession{ID: idSessionID, UserID: idUserID, TenantID: idTenantID, MembershipID: idMembershipID, TokenHash: tokenHash[:], ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	resolved, err := repo.ResolveSession(ctx, tokenHash[:])
	if err != nil || resolved.UserID != idUserID || resolved.TenantID != idTenantID {
		t.Fatalf("resolve session=%#v err=%v", resolved, err)
	}
	if _, err := repo.ResolveSession(ctx, []byte("wrong-token-hash")); err == nil {
		t.Fatal("unknown token hash must not resolve")
	}

	// Agent credentials are stored as verifiers and disabled atomically.
	agentRepo := NewAgentRepository(db)
	secretHash := sha256.Sum256([]byte("agent-secret"))
	agent := auth.Agent{ID: idAgentID, TenantID: idTenantID, Name: "helper", AllowedScopes: []string{"session"}, Capabilities: []string{"observe"}, Status: auth.AgentActive}
	credential := auth.AgentCredential{ID: "cred_" + idAgentID, AgentID: idAgentID, TenantID: idTenantID, SecretHash: secretHash[:]}
	if err := agentRepo.CreateAgent(ctx, agent, credential); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	found, foundCred, err := agentRepo.FindByCredentialHash(ctx, secretHash[:])
	if err != nil || found.ID != idAgentID || foundCred.RevokedAt != nil {
		t.Fatalf("find agent=%#v err=%v", found, err)
	}
	listed, err := agentRepo.ListAgents(ctx, idTenantID)
	if err != nil || len(listed) != 1 || listed[0].Capabilities[0] != "observe" {
		t.Fatalf("list agents=%#v err=%v", listed, err)
	}
	if err := agentRepo.DisableAgent(ctx, idAgentID); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	if _, _, err := agentRepo.FindByCredentialHash(ctx, secretHash[:]); err == nil {
		t.Fatal("disabled agent credential must not resolve")
	}
}
