package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// vectorAxis renders a 512-dimension unit vector aligned to one axis, so two
// nodes on the same axis have cosine similarity 1 and distinct axes 0. The
// embedding model identity is fixed to match between nodes.
func vectorAxis(axis int) string {
	parts := make([]string, VectorDimensions)
	for i := range parts {
		if i == axis {
			parts[i] = "1"
		} else {
			parts[i] = "0"
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func insertEmbeddedNode(t *testing.T, ctx context.Context, db *sql.DB, schema, id, userID string, axis int) {
	t.Helper()
	vector := "NULL"
	if axis >= 0 {
		vector = fmt.Sprintf("'%s'::vector", vectorAxis(axis))
	}
	status := "ready"
	if axis < 0 {
		status = "pending"
	}
	query := fmt.Sprintf(`INSERT INTO %s.memory_nodes
		(id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, confidence,
		 applicability, content, content_text, default_retrieval, provenance,
		 embedding, embedding_model, embedding_artifact, embedding_version, embedding_status)
		VALUES ($1, $2, $3, 'user-global', $3, 'fact', 'active', 0.8,
		 '{}'::jsonb, '{}'::jsonb, $4, true, '{}'::jsonb,
		 %s, 'bge-test', 'artifact', 'v1', $5)`, schema, vector)
	if _, err := db.ExecContext(ctx, query, id, "idem-"+id, userID, "content "+id, status); err != nil {
		t.Fatalf("insert node %s: %v", id, err)
	}
}

func insertTextNode(t *testing.T, ctx context.Context, db *sql.DB, schema, id, userID, contentText string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s.memory_nodes
		(id, idempotency_key, user_id, scope_type, scope_id, memory_type, status, confidence,
		 applicability, content, content_text, default_retrieval, provenance, embedding_status)
		VALUES ($1, $2, $3, 'user-global', $3, 'fact', 'active', 0.8,
		 '{}'::jsonb, '{}'::jsonb, $4, true, '{}'::jsonb, 'pending')`, schema),
		id, "idem-"+id, userID, contentText); err != nil {
		t.Fatalf("insert text node %s: %v", id, err)
	}
}

func TestSemanticNeighborRepositoryScopesAndRequiresEmbeddings(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "semantic neighbor")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repo := NewSemanticNeighborRepository(router)

	const (
		userA  = "00000000-0000-4000-8000-00000000a001"
		userB  = "00000000-0000-4000-8000-00000000b001"
		ref    = "00000000-0000-4000-8000-000000000101"
		near   = "00000000-0000-4000-8000-000000000102"
		far    = "00000000-0000-4000-8000-000000000103"
		otherU = "00000000-0000-4000-8000-000000000104"
		noVec  = "00000000-0000-4000-8000-000000000105"
	)
	insertEmbeddedNode(t, ctx, db, tenant.Schema, ref, userA, 0)
	insertEmbeddedNode(t, ctx, db, tenant.Schema, near, userA, 0)
	insertEmbeddedNode(t, ctx, db, tenant.Schema, far, userA, 1)
	insertEmbeddedNode(t, ctx, db, tenant.Schema, otherU, userB, 0)
	insertEmbeddedNode(t, ctx, db, tenant.Schema, noVec, userA, -1)

	neighbors, err := repo.ListSemanticNeighbors(ctx, tenant.ID, ports.SemanticNeighborQuery{
		ReferenceMemoryID: ref, Threshold: 0.9, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSemanticNeighbors() error = %v", err)
	}
	if len(neighbors) != 1 || neighbors[0].Node.ID != near {
		t.Fatalf("neighbors = %#v, want only the same-axis node %s", neighbors, near)
	}
	if neighbors[0].Similarity < 0.999 {
		t.Fatalf("similarity = %v, want ~1.0", neighbors[0].Similarity)
	}

	// A reference without a ready embedding has no neighbors (degradation).
	empty, err := repo.ListSemanticNeighbors(ctx, tenant.ID, ports.SemanticNeighborQuery{
		ReferenceMemoryID: noVec, Threshold: 0.9, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSemanticNeighbors(no embedding) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("neighbors of an unembedded node = %#v, want none", empty)
	}

	// Invalid queries are rejected rather than defaulted.
	if _, err := repo.ListSemanticNeighbors(ctx, tenant.ID, ports.SemanticNeighborQuery{ReferenceMemoryID: ref, Threshold: 0, Limit: 5}); err == nil {
		t.Fatal("threshold 0 should be rejected")
	}
	if _, err := repo.ListSemanticNeighbors(ctx, tenant.ID, ports.SemanticNeighborQuery{ReferenceMemoryID: ref, Threshold: 0.9, Limit: 0}); err == nil {
		t.Fatal("limit 0 should be rejected")
	}

	// Embedding readiness distinguishes "cannot compare" from "not similar".
	if ready, err := repo.EmbeddingReady(ctx, tenant.ID, ref); err != nil || !ready {
		t.Fatalf("EmbeddingReady(ref) = %v, %v; want true", ready, err)
	}
	if ready, err := repo.EmbeddingReady(ctx, tenant.ID, noVec); err != nil || ready {
		t.Fatalf("EmbeddingReady(noVec) = %v, %v; want false", ready, err)
	}
}

func TestSemanticNeighborRepositoryLexicalFallback(t *testing.T) {
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
	tenant := createMigratedTenant(t, ctx, db, store, "semantic lexical fallback")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	repo := NewSemanticNeighborRepository(router)

	const (
		userID = "00000000-0000-4000-8000-00000000f001"
		left   = "00000000-0000-4000-8000-00000000f002"
		right  = "00000000-0000-4000-8000-00000000f003"
	)
	insertTextNode(t, ctx, db, tenant.Schema, left, userID, "user prefers dark mode interface")
	insertTextNode(t, ctx, db, tenant.Schema, right, userID, "user prefers dark mode interface theme")

	// Without the fallback, an unembedded reference yields nothing.
	withoutFallback, err := repo.ListSemanticNeighbors(ctx, tenant.ID, ports.SemanticNeighborQuery{ReferenceMemoryID: left, Threshold: 0.9, Limit: 10})
	if err != nil {
		t.Fatalf("ListSemanticNeighbors(no fallback) error = %v", err)
	}
	if len(withoutFallback) != 0 {
		t.Fatalf("unembedded reference returned neighbors without fallback: %#v", withoutFallback)
	}

	withFallback, err := repo.ListSemanticNeighbors(ctx, tenant.ID, ports.SemanticNeighborQuery{ReferenceMemoryID: left, Threshold: 0.9, Limit: 10, LexicalFallback: true})
	if err != nil {
		t.Fatalf("ListSemanticNeighbors(fallback) error = %v", err)
	}
	if len(withFallback) != 1 || withFallback[0].Node.ID != right {
		t.Fatalf("lexical fallback = %#v, want the trigram-similar node %s", withFallback, right)
	}
}
