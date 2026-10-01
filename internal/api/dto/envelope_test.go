package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func validEnvelope() Envelope {
	return Envelope{
		Version: CurrentVersion, RequestID: "req-123", IdempotencyKey: "idem-123",
		Principal:  Principal{Type: "agent", ID: "agent-1"},
		Scope:      Scope{TenantID: "tenant-1", UserID: "user-1", Type: "session", SessionID: "session-1"},
		MemoryHint: &MemoryHint{Mode: "focus", Topics: []string{"tenant routing"}, MemoryIDs: []string{"memory-1"}},
		Budget:     Budget{Candidates: 50, Ranking: 25, InjectionTokens: 2048, RelationDepth: 3},
		Privacy:    Privacy{Visibility: "private", AllowExternalLLM: false, SensitiveFields: []string{"email"}},
		Payload:    json.RawMessage(`{"query":"tenant schema routing"}`),
	}
}

func TestEnvelopeV1RoundTripContract(t *testing.T) {
	input := validEnvelope()
	if err := input.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	decoded, err := DecodeEnvelope(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("DecodeEnvelope(): %v", err)
	}
	if decoded.Version != CurrentVersion || decoded.Principal != input.Principal || decoded.Scope != input.Scope || decoded.Budget != input.Budget {
		t.Fatalf("decoded envelope differs: %#v", decoded)
	}
	if string(decoded.Payload) != string(input.Payload) {
		t.Fatalf("payload=%s", decoded.Payload)
	}
}

func TestEnvelopeV1SchemaDocumentsAllRequiredDTOs(t *testing.T) {
	data, err := os.ReadFile("schema_v1.json")
	if err != nil {
		t.Fatalf("read JSON schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse JSON schema: %v", err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected schema dialect: %v", schema["$schema"])
	}
	if schema["additionalProperties"] != false {
		t.Fatal("schema must reject unknown top-level fields")
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required fields missing: %#v", schema["required"])
	}
	got := map[string]bool{}
	for _, value := range required {
		got[value.(string)] = true
	}
	for _, name := range []string{"version", "request_id", "idempotency_key", "principal", "scope", "budget", "privacy", "payload"} {
		if !got[name] {
			t.Errorf("schema missing required envelope property %q", name)
		}
	}
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"principal", "scope", "memory_hint", "budget", "privacy"} {
		if _, exists := properties[name]; !exists {
			t.Errorf("schema missing DTO %q", name)
		}
	}
}

func TestDecodeEnvelopeRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Envelope)
		want   error
	}{
		{"unsupported version", func(e *Envelope) { e.Version = "v2" }, ErrUnsupportedVersion},
		{"missing request id", func(e *Envelope) { e.RequestID = "" }, ErrInvalidEnvelope},
		{"invalid principal", func(e *Envelope) { e.Principal.Type = "workflow" }, ErrInvalidEnvelope},
		{"invalid scope kind", func(e *Envelope) { e.Scope.Type = "project" }, ErrInvalidEnvelope},
		{"session scope without session id", func(e *Envelope) { e.Scope.SessionID = "" }, ErrInvalidEnvelope},
		{"user scope with session id", func(e *Envelope) { e.Scope.Type = "user-global" }, ErrInvalidEnvelope},
		{"invalid hint mode", func(e *Envelope) { e.MemoryHint.Mode = "force" }, ErrInvalidEnvelope},
		{"budget overflow", func(e *Envelope) { e.Budget.InjectionTokens = MaxInjectionTokens + 1 }, ErrInvalidEnvelope},
		{"invalid privacy visibility", func(e *Envelope) { e.Privacy.Visibility = "public" }, ErrInvalidEnvelope},
		{"invalid payload", func(e *Envelope) { e.Payload = json.RawMessage(`{`) }, ErrInvalidEnvelope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := validEnvelope()
			test.mutate(&envelope)
			err := envelope.Validate()
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeEnvelopeRejectsUnknownAndTrailingJSON(t *testing.T) {
	encoded, err := json.Marshal(validEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(nil), encoded[:len(encoded)-1]...)
	unknown = append(unknown, []byte(`,"tenant_override":"evil"}`)...)
	if _, err := DecodeEnvelope(bytes.NewReader(unknown)); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("unknown field error=%v", err)
	}
	trailing := append(append([]byte(nil), encoded...), []byte(` {}`)...)
	if _, err := DecodeEnvelope(bytes.NewReader(trailing)); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("trailing value error=%v", err)
	}
}
