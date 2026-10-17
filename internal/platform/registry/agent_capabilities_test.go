package registry

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"
	"time"

	"github.com/hchw/mengpo/db/migrations"
)

// TestAgentCapabilityBackfillMigration verifies that the platform backfill
// grants session to observe-holders and consolidate to project-holders, and that
// re-running it never appends a duplicate capability.
func TestAgentCapabilityBackfillMigration(t *testing.T) {
	db := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatalf("apply platform migrations: %v", err)
	}
	store := NewStore(db)
	tenant, err := store.RegisterTenant(ctx, "capability backfill tenant")
	if err != nil {
		t.Fatalf("register tenant: %v", err)
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = db.ExecContext(background, `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(background, `DELETE FROM public.agents WHERE tenant_id = $1`, tenant.ID)
		_, _ = db.ExecContext(background, `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})

	observer := "11111111-1111-1111-1111-111111111111"
	projector := "22222222-2222-2222-2222-222222222222"
	if _, err := db.ExecContext(ctx, `INSERT INTO public.agents (id, tenant_id, name, allowed_scopes, capabilities, status)
		VALUES ($1, $2, 'legacy-observer', ARRAY['session'], ARRAY['observe'], 'active'),
		       ($3, $2, 'legacy-projector', ARRAY['session'], ARRAY['project'], 'active')`,
		observer, tenant.ID, projector); err != nil {
		t.Fatalf("insert legacy agents: %v", err)
	}

	backfill := readPlatformMigration(t, "000005_agent_capabilities.sql")
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := db.ExecContext(ctx, backfill); err != nil {
			t.Fatalf("run backfill attempt %d: %v", attempt, err)
		}
	}

	if got := capabilityCount(t, ctx, db, observer, "session"); got != 1 {
		t.Fatalf("observer session count = %d, want 1 after repeated runs", got)
	}
	if got := capabilityCount(t, ctx, db, observer, "consolidate"); got != 0 {
		t.Fatalf("observer consolidate count = %d, want 0", got)
	}
	if got := capabilityCount(t, ctx, db, projector, "consolidate"); got != 1 {
		t.Fatalf("projector consolidate count = %d, want 1 after repeated runs", got)
	}
	if got := capabilityCount(t, ctx, db, projector, "session"); got != 0 {
		t.Fatalf("projector session count = %d, want 0", got)
	}
}

func readPlatformMigration(t *testing.T, name string) string {
	t.Helper()
	contents, err := fs.ReadFile(migrations.PlatformFS(), name)
	if err != nil {
		t.Fatalf("read platform migration %q: %v", name, err)
	}
	return string(contents)
}

func capabilityCount(t *testing.T, ctx context.Context, db *sql.DB, agentID, capability string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.agents a, unnest(a.capabilities) AS c WHERE a.id = $1 AND c = $2`,
		agentID, capability).Scan(&count); err != nil {
		t.Fatalf("count capability %q: %v", capability, err)
	}
	return count
}
