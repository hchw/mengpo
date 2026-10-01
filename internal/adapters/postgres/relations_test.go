package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRelationRecallExpandsSeedsWithinTenantSchema(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "relation recall tenant")
	other := createMigratedTenant(t, ctx, db, store, "relation recall other tenant")
	t.Cleanup(func() {
		for _, id := range []string{tenant.ID, other.ID} {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, id)
		}
	})
	router := tenantdb.NewRouter(db, store)
	memories := NewMemoryRepository(router)
	relations := NewRelationRepository(router)

	userID := "00000000-0000-4000-8000-000000000031"
	seedID := "00000000-0000-4000-8000-000000000631"
	targetID := "00000000-0000-4000-8000-000000000632"
	unrelatedID := "00000000-0000-4000-8000-000000000633"
	deletedTargetID := "00000000-0000-4000-8000-000000000634"
	mustCreate := func(tenantID, id, text string) ports.MemoryNodeRecord {
		t.Helper()
		record, err := memories.Create(ctx, tenantID, ports.MemoryNodeRecord{
			ID:               id,
			IdempotencyKey:   "relation-" + id,
			UserID:           userID,
			ScopeType:        "user-global",
			ScopeID:          userID,
			MemoryType:       "fact",
			Status:           "stable",
			Confidence:       0.8,
			Applicability:    json.RawMessage(`{}`),
			Content:          json.RawMessage(`{"text":"relation fixture"}`),
			ContentText:      text,
			DefaultRetrieval: true,
		})
		if err != nil {
			t.Fatalf("create memory %s: %v", id, err)
		}
		return record
	}
	mustCreate(tenant.ID, seedID, "租户路由的种子记忆")
	mustCreate(tenant.ID, targetID, "关系指向的相邻记忆")
	mustCreate(tenant.ID, unrelatedID, "完全无关的记忆")
	mustCreate(tenant.ID, deletedTargetID, "已删除的关系目标")
	mustCreate(other.ID, "00000000-0000-4000-8000-000000000635", "其他租户的记忆")
	// The other tenant also links its memory so cross-schema traversal can be detected.
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO memory_relations (id, source_memory_id, target_memory_id, relation_type, confidence)
			VALUES
				('00000000-0000-4000-8000-000000000641', $1, $2, 'relates_to', 0.9),
				('00000000-0000-4000-8000-000000000642', $3, $1, 'relates_to', 0.6),
				('00000000-0000-4000-8000-000000000643', $1, $4, 'relates_to', 0.8)`,
			seedID, targetID, unrelatedID, deletedTargetID)
		return err
	}); err != nil {
		t.Fatalf("insert relations: %v", err)
	}
	if _, err := memories.Get(ctx, tenant.ID, deletedTargetID); err == nil {
		// soft delete the fourth memory through governance-free path for the fixture
		if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE memory_nodes SET deleted_at = now() WHERE id = $1`, deletedTargetID)
			return err
		}); err != nil {
			t.Fatalf("soft delete relation target: %v", err)
		}
	}
	related, err := relations.ListRelated(ctx, tenant.ID, []string{seedID}, 10)
	if err != nil {
		t.Fatalf("ListRelated(): %v", err)
	}
	ids := map[string]bool{}
	for _, item := range related {
		ids[item.Node.ID] = true
		if item.Node.UserID != userID {
			t.Fatalf("related memory escaped tenant scope: %#v", item.Node)
		}
	}
	if !ids[targetID] {
		t.Fatalf("forward relation target missing from expansion: %v", ids)
	}
	if !ids[unrelatedID] {
		t.Fatalf("reverse relation source missing from expansion: %v", ids)
	}
	if ids[deletedTargetID] {
		t.Fatal("deleted memory surfaced through relation expansion")
	}
	if len(related) != 2 {
		t.Fatalf("related = %d, want exactly the two live neighbors", len(related))
	}
	// Cross-tenant: the other tenant's seeds must not reach this schema's memories.
	otherRelated, err := relations.ListRelated(ctx, other.ID, []string{"00000000-0000-4000-8000-000000000635"}, 10)
	if err != nil {
		t.Fatalf("ListRelated(other tenant): %v", err)
	}
	if len(otherRelated) != 0 {
		t.Fatalf("cross-tenant relation leak: %#v", otherRelated)
	}
	// Empty seed list and tenant are safe no-ops.
	if none, err := relations.ListRelated(ctx, tenant.ID, nil, 10); err != nil || len(none) != 0 {
		t.Fatalf("ListRelated(nil seeds) = %v, %v", none, err)
	}
}
