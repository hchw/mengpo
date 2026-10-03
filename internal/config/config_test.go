package config

import (
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadFromUsesSafeDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookup(map[string]string{
		"DATABASE_URL": "postgres://memory:memory@localhost:5432/memory?sslmode=disable",
	}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Environment != "development" || cfg.HTTPAddr != ":8080" || cfg.Auth.SessionTTL != 12*time.Hour {
		t.Fatalf("unexpected service defaults: %#v", cfg)
	}
	if cfg.Auth.SessionCookieSecure || cfg.Security.AllowExternalLLMAnalysis {
		t.Fatal("development and external-model privacy defaults are unsafe")
	}
	if cfg.Providers.MemoryLLM.Enabled || cfg.Providers.Embedding.Enabled || cfg.Providers.Reranker.Enabled {
		t.Fatal("optional providers must be disabled until explicitly configured")
	}
	if cfg.Providers.Embedding.ModelID != DefaultEmbeddingModel || cfg.Providers.Embedding.Dimensions != DefaultEmbeddingDim {
		t.Fatalf("unexpected embedding defaults: %#v", cfg.Providers.Embedding)
	}
	if cfg.Queue.Adapter != "nats-core" || cfg.Scopes[0] != "user-global" || cfg.Scopes[1] != "session" {
		t.Fatalf("unexpected queue/scope defaults: %#v / %#v", cfg.Queue, cfg.Scopes)
	}
}

func TestLoadFromRequiresDatabaseURL(t *testing.T) {
	_, err := LoadFrom(lookup(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("LoadFrom() error = %v, want required DATABASE_URL", err)
	}
}

func TestProductionRequiresSecureSessionCookie(t *testing.T) {
	_, err := LoadFrom(lookup(map[string]string{
		"APP_ENV":            "production",
		"DATABASE_URL":       "postgres://memory:memory@db:5432/memory",
		"AUTH_COOKIE_SECURE": "false",
	}))
	if err == nil || !strings.Contains(err.Error(), "cannot be disabled") {
		t.Fatalf("LoadFrom() error = %v, want secure-cookie rejection", err)
	}

	cfg, err := LoadFrom(lookup(map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://memory:memory@db:5432/memory",
	}))
	if err != nil {
		t.Fatalf("production secure-cookie default: %v", err)
	}
	if !cfg.Auth.SessionCookieSecure {
		t.Fatal("secure cookies must default on in production")
	}
}

func TestEnabledProvidersRequireConfiguration(t *testing.T) {
	_, err := LoadFrom(lookup(map[string]string{
		"DATABASE_URL":       "postgres://memory:memory@localhost/memory",
		"MEMORY_LLM_ENABLED": "true",
	}))
	if err == nil || !strings.Contains(err.Error(), "MEMORY_LLM_BASE_URL") {
		t.Fatalf("LoadFrom() error = %v, want Memory LLM configuration error", err)
	}
}

func TestLoadFromRejectsInvalidScopeAndMQConfig(t *testing.T) {
	_, err := LoadFrom(lookup(map[string]string{
		"DATABASE_URL": "postgres://memory:memory@localhost/memory",
		"MQ_ADAPTER":   "unknown",
	}))
	if err == nil || !strings.Contains(err.Error(), "MQ_ADAPTER") {
		t.Fatalf("LoadFrom() error = %v, want invalid MQ adapter error", err)
	}

	cfg, err := LoadFrom(lookup(map[string]string{
		"DATABASE_URL": "postgres://memory:memory@localhost/memory",
		"MQ_ADAPTER":   "none",
	}))
	if err != nil {
		t.Fatalf("PostgreSQL polling-only configuration should be allowed: %v", err)
	}
	cfg.Scopes = []string{"project"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "only user-global and session") {
		t.Fatalf("Validate() error = %v, want unsupported scope rejection", err)
	}
}

func TestLoadFromParsesTimeoutAndRequestSize(t *testing.T) {
	cfg, err := LoadFrom(lookup(map[string]string{
		"DATABASE_URL":                "postgres://memory:memory@localhost/memory",
		"AUTH_SESSION_TTL":            "2h",
		"HTTP_REQUEST_TIMEOUT":        "10s",
		"MAX_REQUEST_BYTES":           "2048",
		"ALLOW_EXTERNAL_LLM_ANALYSIS": "true",
	}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Auth.SessionTTL != 2*time.Hour || cfg.RequestTimeout != 10*time.Second || cfg.Security.MaxRequestBytes != 2048 {
		t.Fatalf("parsed config values are incorrect: %#v", cfg)
	}
	if !cfg.Security.AllowExternalLLMAnalysis {
		t.Fatal("explicit privacy opt-in should be honored")
	}
}
