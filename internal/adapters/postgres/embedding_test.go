package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEmbeddingMigrationWriteAndScopedVectorSearch(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL/pgvector integration tests")
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
	tenant := createMigratedTenant(t, ctx, db, store, "embedding tenant")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id = $1`, tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	memories := NewMemoryRepository(router)
	embeddings := NewEmbeddingRepository(router)
	userID := "00000000-0000-4000-8000-000000000001"
	otherUserID := "00000000-0000-4000-8000-000000000002"
	sessionID := "00000000-0000-4000-8000-000000000011"
	otherSessionID := "00000000-0000-4000-8000-000000000012"
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		for _, id := range []string{sessionID, otherSessionID} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, status) VALUES ($1, $2, 'active')`, id, userID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	makeVector := func(index int) []float32 {
		vector := make([]float32, VectorDimensions)
		vector[index] = 1
		return vector
	}
	fixtures := []struct {
		id, idempotency, owner, session string
		scope, scopeID, status          string
		vector                          []float32
	}{
		{"00000000-0000-4000-8000-000000000501", "embedding:near", userID, "", "user-global", userID, "stable", makeVector(0)},
		{"00000000-0000-4000-8000-000000000502", "embedding:far", userID, "", "user-global", userID, "stable", makeVector(1)},
		{"00000000-0000-4000-8000-000000000503", "embedding:session", userID, sessionID, "session", sessionID, "active", makeVector(2)},
		{"00000000-0000-4000-8000-000000000504", "embedding:other-session", userID, otherSessionID, "session", otherSessionID, "stable", makeVector(0)},
		{"00000000-0000-4000-8000-000000000505", "embedding:other-user", otherUserID, "", "user-global", otherUserID, "stable", makeVector(0)},
		{"00000000-0000-4000-8000-000000000506", "embedding:candidate", userID, "", "user-global", userID, "candidate", makeVector(0)},
	}
	for _, fixture := range fixtures {
		_, err := memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
			ID: fixture.id, IdempotencyKey: fixture.idempotency, UserID: fixture.owner,
			SessionID: fixture.session, ScopeType: fixture.scope, ScopeID: fixture.scopeID,
			MemoryType: "fact", Status: fixture.status, Confidence: 0.8,
			Applicability: json.RawMessage(`{}`), Content: json.RawMessage(`{"text":"embedding fixture"}`),
			ContentText: "embedding fixture", DefaultRetrieval: true,
		})
		if err != nil {
			t.Fatalf("create memory fixture: %v", err)
		}
		if err := embeddings.SaveEmbedding(ctx, tenant.ID, fixture.id, "all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "q8-v1", fixture.vector); err != nil {
			t.Fatalf("SaveEmbedding(%s): %v", fixture.id, err)
		}
	}

	nearest, err := embeddings.SearchSimilar(ctx, tenant.ID, userID, sessionID,
		"all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "q8-v1", makeVector(0), 10)
	if err != nil {
		t.Fatalf("SearchSimilar() error = %v", err)
	}
	if len(nearest) != 3 {
		t.Fatalf("vector search returned %d candidates, want 3: %#v", len(nearest), nearest)
	}
	if nearest[0].Node.ID != fixtures[0].id || nearest[0].Distance > 0.001 {
		t.Fatalf("nearest vector result = %#v, want exact matching memory first", nearest[0])
	}
	for _, result := range nearest {
		if result.Node.UserID != userID || result.Node.SessionID != "" && result.Node.SessionID != sessionID {
			t.Fatalf("vector search returned out-of-scope memory: %#v", result.Node)
		}
	}
	wrongVersion, err := embeddings.SearchSimilar(ctx, tenant.ID, userID, sessionID,
		"all-MiniLM-L6-v2", "all-MiniLM-L6-v2-Q8_0.gguf", "other-version", makeVector(0), 10)
	if err != nil {
		t.Fatalf("SearchSimilar(other version): %v", err)
	}
	if len(wrongVersion) != 0 {
		t.Fatalf("vector search mixed model versions: %#v", wrongVersion)
	}

	var modelID, artifact, version, status string
	if err := router.WithTenantTx(ctx, tenant.ID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT embedding_model, embedding_artifact, embedding_version, embedding_status FROM memory_nodes WHERE id = $1`, fixtures[0].id).
			Scan(&modelID, &artifact, &version, &status)
	}); err != nil {
		t.Fatalf("read embedding metadata: %v", err)
	}
	if modelID != "all-MiniLM-L6-v2" || artifact != "all-MiniLM-L6-v2-Q8_0.gguf" || version != "q8-v1" || status != "ready" {
		t.Fatalf("embedding metadata = %q/%q/%q/%q", modelID, artifact, version, status)
	}
	var indexDefinition string
	if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_catalog.pg_indexes WHERE schemaname = $1 AND indexname = 'memory_nodes_embedding_hnsw_idx'`, tenant.Schema).Scan(&indexDefinition); err != nil {
		t.Fatalf("load HNSW index definition: %v", err)
	}
	if !strings.Contains(indexDefinition, "USING hnsw") || !strings.Contains(indexDefinition, "vector_cosine_ops") {
		t.Fatalf("unexpected HNSW index definition: %s", indexDefinition)
	}

	if err := embeddings.SaveEmbedding(ctx, tenant.ID, fixtures[0].id, "model", "artifact", "version", []float32{1}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("invalid dimensions error = %v, want ErrInvalidVector", err)
	}
	invalid := makeVector(0)
	invalid[1] = float32(math.NaN())
	if err := embeddings.SaveEmbedding(ctx, tenant.ID, fixtures[0].id, "model", "artifact", "version", invalid); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("non-finite vector error = %v, want ErrInvalidVector", err)
	}
}
