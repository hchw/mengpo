package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestSecurityLoggerEmitsEveryCategoryWithTenantAndTrace(t *testing.T) {
	var buffer bytes.Buffer
	logger := NewSecurityLogger(slog.New(slog.NewJSONHandler(&buffer, nil)))
	ctx := context.WithValue(context.Background(), requestIDKey, "req-1")
	ctx = context.WithValue(ctx, traceIDKey, "trace-1")
	categories := []SecurityEventType{SecurityProvider, SecurityAPI, SecurityDataAccess, SecurityExport, SecurityDelete, SecurityAudit}
	for _, category := range categories {
		if err := logger.Log(ctx, SecurityEvent{Type: category, ActorType: "user", ActorID: "user-1", TenantID: "tenant-a", Action: "read", Outcome: "allowed"}); err != nil {
			t.Fatalf("log %s: %v", category, err)
		}
	}
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != len(categories) {
		t.Fatalf("emitted %d lines, want %d", len(lines), len(categories))
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["tenant_id"] != "tenant-a" || record["actor_id"] != "user-1" || record["request_id"] != "req-1" || record["trace_id"] != "trace-1" {
			t.Fatalf("record missing tenant/actor/trace: %v", record)
		}
	}
}

func TestSecurityLoggerRedactsSensitiveDetailAndRequiresIdentity(t *testing.T) {
	var buffer bytes.Buffer
	logger := NewSecurityLogger(slog.New(slog.NewJSONHandler(&buffer, nil)))
	if err := logger.Log(context.Background(), SecurityEvent{Type: SecurityProvider, ActorID: "a", TenantID: "t", Outcome: "allowed", Detail: "api_key=sk-123"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buffer.String(), "sk-123") || !strings.Contains(buffer.String(), "[redacted]") {
		t.Fatalf("sensitive detail leaked: %s", buffer.String())
	}
	if err := logger.Log(context.Background(), SecurityEvent{Type: SecurityProvider, ActorID: "", TenantID: "t", Outcome: "allowed"}); !errors.Is(err, ErrIncompleteSecurityEvent) {
		t.Fatalf("missing actor err=%v", err)
	}
	if err := logger.Log(context.Background(), SecurityEvent{Type: SecurityProvider, ActorID: "a", TenantID: "", Outcome: "allowed"}); !errors.Is(err, ErrIncompleteSecurityEvent) {
		t.Fatalf("missing tenant err=%v", err)
	}
}

func TestExportAndDeleteAuthorizeTenantActorAndReason(t *testing.T) {
	if err := AuthorizeExport(ExportRequest{TenantID: "t", ActorID: "a", Format: "jsonl"}); err != nil {
		t.Fatalf("valid export: %v", err)
	}
	if err := AuthorizeExport(ExportRequest{TenantID: "t", ActorID: "a", Format: "pdf"}); err == nil {
		t.Fatal("unsupported export format accepted")
	}
	if err := AuthorizeExport(ExportRequest{TenantID: "", ActorID: "a", Format: "jsonl"}); !errors.Is(err, ErrIncompleteSecurityEvent) {
		t.Fatalf("anonymous export err=%v", err)
	}
	if err := AuthorizeDelete(DeleteRequest{TenantID: "t", ActorID: "a", MemoryID: "m", Reason: "user request"}); err != nil {
		t.Fatalf("valid delete: %v", err)
	}
	if err := AuthorizeDelete(DeleteRequest{TenantID: "t", ActorID: "a", MemoryID: "m"}); err == nil {
		t.Fatal("delete without reason accepted")
	}
}
