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

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSearchScopesFiltersPaginatesAndUsesFullTextIndex(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "search tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repo := NewMemoryRepository(router)
	search := NewSearchRepository(router)
	userID := "00000000-0000-4000-8000-000000000001"
	otherUserID := "00000000-0000-4000-8000-000000000002"
	sessionID := "00000000-0000-4000-8000-000000000011"
	otherSessionID := "00000000-0000-4000-8000-000000000012"
	foreignSessionID := "00000000-0000-4000-8000-000000000013"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		for _, id := range []string{sessionID, otherSessionID} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1, $2, 'active')`, id, userID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1, $2, 'active')`, foreignSessionID, otherUserID)
		return err
	}); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	nodes := []ports.MemoryNodeRecord{
		{ID: "00000000-0000-4000-8000-000000000401", IdempotencyKey: "search:g1", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "experience", Status: "stable", Confidence: 0.9, ContentText: "database reliability and transaction recovery", Content: json.RawMessage(`{"text":"database reliability and transaction recovery"}`), DefaultRetrieval: true},
		{ID: "00000000-0000-4000-8000-000000000402", IdempotencyKey: "search:g2", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "preference", Status: "active", Confidence: 0.8, ContentText: "database query output should be concise", Content: json.RawMessage(`{"text":"database query output should be concise"}`), DefaultRetrieval: true},
		{ID: "00000000-0000-4000-8000-000000000403", IdempotencyKey: "search:s1", UserID: userID, SessionID: sessionID, ScopeType: "session", ScopeID: sessionID, MemoryType: "fact", Status: "active", Confidence: 0.7, ContentText: "database retry attempt is running", Content: json.RawMessage(`{"text":"database retry attempt is running"}`), DefaultRetrieval: true},
		{ID: "00000000-0000-4000-8000-000000000404", IdempotencyKey: "search:s2", UserID: userID, SessionID: otherSessionID, ScopeType: "session", ScopeID: otherSessionID, MemoryType: "fact", Status: "stable", Confidence: 0.99, ContentText: "database from a different session", Content: json.RawMessage(`{"text":"database from a different session"}`), DefaultRetrieval: true},
		{ID: "00000000-0000-4000-8000-000000000405", IdempotencyKey: "search:other-user", UserID: otherUserID, ScopeType: "user-global", ScopeID: otherUserID, MemoryType: "experience", Status: "stable", Confidence: 1, ContentText: "database for another user", Content: json.RawMessage(`{"text":"database for another user"}`), DefaultRetrieval: true},
		{ID: "00000000-0000-4000-8000-000000000406", IdempotencyKey: "search:candidate", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "experience", Status: "candidate", Confidence: 1, ContentText: "database candidate must not appear", Content: json.RawMessage(`{"text":"database candidate must not appear"}`), DefaultRetrieval: true},
		{ID: "00000000-0000-4000-8000-000000000407", IdempotencyKey: "search:expired", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "experience", Status: "stable", Confidence: 1, ContentText: "database expired memory", Content: json.RawMessage(`{"text":"database expired memory"}`), DefaultRetrieval: true},
	}
	for _, node := range nodes {
		if _, err := repo.Create(ctx, tenant.ID, node); err != nil {
			t.Fatalf("create search fixture %s: %v", node.ID, err)
		}
	}
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE memory_nodes SET expires_at = now() - interval '1 hour' WHERE id = $1`, nodes[6].ID)
		return err
	}); err != nil {
		t.Fatalf("expire fixture: %v", err)
	}

	first, err := search.Search(ctx, tenant.ID, userID, sessionID, ports.MemorySearchRequest{Query: "database", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("Search(page 1) error = %v", err)
	}
	second, err := search.Search(ctx, tenant.ID, userID, sessionID, ports.MemorySearchRequest{Query: "database", Page: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("Search(page 2) error = %v", err)
	}
	if first.Total != 3 || second.Total != 3 || len(first.Items) != 1 || len(second.Items) != 1 {
		t.Fatalf("unexpected search pages: first=%#v second=%#v", first, second)
	}
	if first.Items[0].Node.ID == second.Items[0].Node.ID {
		t.Fatal("pagination returned a duplicate memory node")
	}
	for _, page := range []ports.MemorySearchPage{first, second} {
		for _, item := range page.Items {
			if item.Node.UserID != userID || item.Node.SessionID != "" && item.Node.SessionID != sessionID {
				t.Fatalf("search returned out-of-scope node: %#v", item.Node)
			}
		}
	}

	preferences, err := search.Search(ctx, tenant.ID, userID, sessionID, ports.MemorySearchRequest{MemoryType: "preference"})
	if err != nil {
		t.Fatalf("structured type filter: %v", err)
	}
	if preferences.Total != 1 || len(preferences.Items) != 1 || preferences.Items[0].Node.ID != nodes[1].ID {
		t.Fatalf("type filter results = %#v", preferences)
	}
	globalOnly, err := search.Search(ctx, tenant.ID, userID, "", ports.MemorySearchRequest{Query: "database"})
	if err != nil {
		t.Fatalf("global-only search: %v", err)
	}
	if globalOnly.Total != 2 {
		t.Fatalf("global-only search total = %d, want 2", globalOnly.Total)
	}
	foreignSessionSearch, err := search.Search(ctx, tenant.ID, userID, foreignSessionID, ports.MemorySearchRequest{Query: "database"})
	if err != nil {
		t.Fatalf("search with foreign session: %v", err)
	}
	if foreignSessionSearch.Total != 0 {
		t.Fatalf("foreign session returned %d results, want none", foreignSessionSearch.Total)
	}
	if _, err := search.Search(ctx, tenant.ID, userID, sessionID, ports.MemorySearchRequest{PageSize: MaxSearchPageSize + 1}); !errors.Is(err, ErrInvalidSearchPage) {
		t.Fatalf("oversized page error = %v, want ErrInvalidSearchPage", err)
	}

	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `EXPLAIN (COSTS OFF) SELECT id FROM memory_nodes
			WHERE to_tsvector('simple', coalesce(content_text, '')) @@ plainto_tsquery('simple', 'database')`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !strings.Contains(plan.String(), "memory_nodes_content_fts_idx") {
			return errors.New("EXPLAIN did not select the full-text GIN index: " + plan.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("full-text index EXPLAIN: %v", err)
	}
}

func TestSearchMatchesAnyTermIncludingCJK(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "search cjk tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repo := NewMemoryRepository(router)
	search := NewSearchRepository(router)
	userID := "00000000-0000-4000-8000-000000000001"
	pgvectorNode := ports.MemoryNodeRecord{ID: "00000000-0000-4000-8000-000000000501", IdempotencyKey: "search-cjk:pgvector", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "preference", Status: "active", Confidence: 0.9, ContentText: "本地开发一律用 pgvector 存向量,不用 faiss", Content: json.RawMessage(`{"text":"本地开发一律用 pgvector 存向量,不用 faiss"}`), DefaultRetrieval: true}
	redisNode := ports.MemoryNodeRecord{ID: "00000000-0000-4000-8000-000000000502", IdempotencyKey: "search-cjk:redis", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "preference", Status: "active", Confidence: 0.9, ContentText: "redis 只做缓存和限流", Content: json.RawMessage(`{"text":"redis 只做缓存和限流"}`), DefaultRetrieval: true}
	tailwindNode := ports.MemoryNodeRecord{ID: "00000000-0000-4000-8000-000000000503", IdempotencyKey: "search-cjk:tailwind", UserID: userID, ScopeType: "user-global", ScopeID: userID, MemoryType: "preference", Status: "active", Confidence: 0.9, ContentText: "前端样式统一用 tailwindcss v4 的 @theme 令牌", Content: json.RawMessage(`{"text":"前端样式统一用 tailwindcss v4 的 @theme 令牌"}`), DefaultRetrieval: true}
	for _, node := range []ports.MemoryNodeRecord{pgvectorNode, redisNode, tailwindNode} {
		if _, err := repo.Create(ctx, tenant.ID, node); err != nil {
			t.Fatalf("create fixture %s: %v", node.ID, err)
		}
	}
	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{"cjk substring matches", "本地开发", 1},
		{"cjk contiguous run matches", "存向量", 1},
		{"english partial prompt still matches", "which database should I use, pgvector or faiss?", 1},
		{"multi-term query matches both memories", "pgvector redis", 2},
		{"english prefix matches compound", "tailwind", 1},
		{"unrelated terms match nothing", "kubernetes auditing", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := search.Search(ctx, tenant.ID, userID, "", ports.MemorySearchRequest{Query: tc.query, Page: 1, PageSize: 20})
			if err != nil {
				t.Fatalf("Search(%q) error = %v", tc.query, err)
			}
			if page.Total != tc.want {
				t.Fatalf("Search(%q) total = %d, want %d", tc.query, page.Total, tc.want)
			}
		})
	}
}
