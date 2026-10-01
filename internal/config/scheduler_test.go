package config

import (
	"testing"
	"time"
)

func baseEnv(extra map[string]string) LookupEnv {
	values := map[string]string{
		"MEMORY_DATABASE_URL": "postgres://memory:memory@localhost:5432/memory?sslmode=disable",
		"MEMORY_HTTP_ADDR":    ":8080",
	}
	for key, value := range extra {
		values[key] = value
	}
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestSchedulerDefaults(t *testing.T) {
	cfg, err := LoadFrom(baseEnv(nil))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Scheduler.Adapter != "internal" {
		t.Fatalf("Scheduler.Adapter = %q, want internal", cfg.Scheduler.Adapter)
	}
	if cfg.Scheduler.DefaultInterval != 24*time.Hour {
		t.Fatalf("Scheduler.DefaultInterval = %v, want 24h", cfg.Scheduler.DefaultInterval)
	}
}

func TestSchedulerAdapterValidation(t *testing.T) {
	if _, err := LoadFrom(baseEnv(map[string]string{"MEMORY_SCHEDULER_ADAPTER": "none"})); err != nil {
		t.Fatalf("LoadFrom(none) error = %v", err)
	}
	if _, err := LoadFrom(baseEnv(map[string]string{"MEMORY_SCHEDULER_ADAPTER": "bogus"})); err == nil {
		t.Fatal("expected unsupported scheduler adapter to fail fast")
	}
}

func TestAnalysisAndLLMDefaults(t *testing.T) {
	cfg, err := LoadFrom(baseEnv(nil))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Providers.MemoryLLM.Timeout != 30*time.Second || cfg.Providers.MemoryLLM.MaxAttempts != 3 {
		t.Fatalf("MemoryLLM defaults = %+v", cfg.Providers.MemoryLLM)
	}
	if cfg.Analysis.MaxTokens != 2048 || cfg.Analysis.MaintenanceBatchSize != 50 {
		t.Fatalf("Analysis defaults = %+v", cfg.Analysis)
	}
}

func TestLLMTimeoutMustBePositive(t *testing.T) {
	if _, err := LoadFrom(baseEnv(map[string]string{"MEMORY_LLM_TIMEOUT": "-1s"})); err == nil {
		t.Fatal("expected non-positive LLM timeout to fail")
	}
	if _, err := LoadFrom(baseEnv(map[string]string{"MEMORY_LLM_MAX_ATTEMPTS": "0"})); err == nil {
		t.Fatal("expected zero max attempts to fail")
	}
}
