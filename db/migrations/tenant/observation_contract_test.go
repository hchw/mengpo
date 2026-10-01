package tenant_test

import (
	"os"
	"strings"
	"testing"
)

func TestTenantMigrationDefinesObservedEventContract(t *testing.T) {
	migration, err := os.ReadFile("000001_tenant_memory_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(migration)
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS observed_events")
	end := strings.Index(sql[start:], ");")
	if start < 0 || end < 0 {
		t.Fatal("observed_events table definition not found")
	}
	definition := sql[start : start+end]
	for _, column := range []string{"idempotency_key", "source_type", "source_id", "sequence", "parent_event_id", "reliability", "occurred_at", "ingested_at", "retention_class", "normalization_status", "analysis_status", "redaction_status"} {
		if !strings.Contains(definition, column) {
			t.Errorf("observed_events contract missing %q", column)
		}
	}
	if !strings.Contains(definition, "UNIQUE (source_type, source_id, source_event_id)") || !strings.Contains(definition, "idempotency_key text NOT NULL UNIQUE") {
		t.Fatal("observed_events migration must enforce source and idempotency uniqueness")
	}
}
