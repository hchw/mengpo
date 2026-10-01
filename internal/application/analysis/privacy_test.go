package analysis

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

func TestPrepareBatchRedactsSensitiveFieldsAndKeepsMetadata(t *testing.T) {
	input := validBatch()
	input.Events[0].Payload = json.RawMessage(`{"text":"hello","api_key":"secret","password":"pw","extra":"remove"}`)
	got, err := PrepareBatch(input, PrivacyPolicy{AllowedFields: map[string]bool{"text": true, "api_key": true, "password": true}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(got.Events[0].Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["text"] != "hello" || fields["api_key"] != "[REDACTED]" || fields["password"] != "[REDACTED]" {
		t.Fatalf("filtered data=%v", fields)
	}
	if _, ok := fields["extra"]; ok {
		t.Fatal("unapproved event field leaked")
	}
	if got.TenantID != "tenant-a" || got.Metadata["tenant_id"] != "tenant-a" || got.Metadata["prompt_version"] != "prompt-v1" || got.Metadata["schema_version"] != "schema-v1" {
		t.Fatalf("metadata=%#v", got)
	}
	if string(input.Events[0].Payload) == string(got.Events[0].Payload) {
		t.Fatal("input batch was not transformed")
	}
}

func TestPrepareBatchCanRejectSensitiveInput(t *testing.T) {
	input := validBatch()
	input.Events[0].Payload = json.RawMessage(`{"authorization":"Bearer secret"}`)
	if _, err := PrepareBatch(input, PrivacyPolicy{RejectSensitive: true}); !errors.Is(err, ErrSensitiveInput) {
		t.Fatalf("error=%v", err)
	}
}

func TestPrepareBatchRequiresTenantAndVersions(t *testing.T) {
	if _, err := PrepareBatch(ports.AnalysisBatch{}, PrivacyPolicy{}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("error=%v", err)
	}
}
