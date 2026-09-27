package tenantlifecycle

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	pgadapter "github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestExportTenantStreamsOnlyAuthorizedActiveTenantData(t *testing.T) {
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
	tenant, err := store.ProvisionTenant(ctx, "export tenant")
	if err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	t.Cleanup(func() {
		_ = store.PauseTenant(context.Background(), tenant.ID)
		_ = store.DeleteTenant(context.Background(), tenant.ID)
	})
	router := tenantdb.NewRouter(db, store)
	memories := pgadapter.NewMemoryRepository(router)
	userID := "00000000-0000-4000-8000-000000000001"
	_, err = memories.Create(ctx, tenant.ID, ports.MemoryNodeRecord{
		ID: "00000000-0000-4000-8000-000000000701", IdempotencyKey: "export:memory:1",
		UserID: userID, ScopeType: "user-global", ScopeID: userID,
		MemoryType: "preference", Status: "stable", Confidence: 0.9,
		Applicability: json.RawMessage(`{}`), Content: json.RawMessage(`{"text":"exportable memory"}`),
		ContentText: "exportable memory", DefaultRetrieval: true,
	})
	if err != nil {
		t.Fatalf("create export fixture: %v", err)
	}

	service := NewService(router)
	principal := tenants.ActiveTenantContext{
		UserID: "00000000-0000-4000-8000-000000000099",
		Tenant: tenant, Permissions: []string{PermissionExportTenant},
	}
	var output bytes.Buffer
	if err := service.ExportTenant(ctx, principal, &output); err != nil {
		t.Fatalf("ExportTenant() error = %v", err)
	}
	scanner := bufio.NewScanner(&output)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNumber := 0
	memoryRows := 0
	for scanner.Scan() {
		lineNumber++
		var record map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("invalid JSONL record %d: %v", lineNumber, err)
		}
		var recordType string
		if err := json.Unmarshal(record["type"], &recordType); err != nil {
			t.Fatalf("record %d lacks type: %v", lineNumber, err)
		}
		if lineNumber == 1 {
			var header exportHeader
			if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
				t.Fatal(err)
			}
			if header.Type != "header" || header.FormatVersion != 1 || header.TenantID != tenant.ID {
				t.Fatalf("unexpected export header: %#v", header)
			}
			continue
		}
		var row exportRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Table == "tenant_migrations" {
			t.Fatal("internal migration ledger must not be included in logical data export")
		}
		if row.Table == "memory_nodes" {
			memoryRows++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lineNumber < 2 || memoryRows != 1 {
		t.Fatalf("export produced %d lines and %d memory rows", lineNumber, memoryRows)
	}

	var forbidden bytes.Buffer
	principal.Permissions = nil
	if err := service.ExportTenant(ctx, principal, &forbidden); err != ErrForbidden {
		t.Fatalf("export without permission = %v, want ErrForbidden", err)
	}
	principal.Permissions = []string{PermissionExportTenant}
	principal.Tenant.Status = auth.TenantSuspended
	if err := service.ExportTenant(ctx, principal, &forbidden); err != ErrForbidden {
		t.Fatalf("export suspended tenant = %v, want ErrForbidden", err)
	}
}
