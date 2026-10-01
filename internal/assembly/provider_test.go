package assembly

import (
	"context"
	"testing"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/providerconfig"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/ports"
)

// TestProviderBuilderHonoursExternalFlag covers the two assembly states: a
// disabled provider (or disabled external analysis) degrades to the rule
// baseline, while an enabled provider installs the resilient external analyst.
func TestProviderBuilderHonoursExternalFlag(t *testing.T) {
	disabled := newProviderAnalystBuilder(config.Config{Security: config.SecurityConfig{AllowExternalLLMAnalysis: false}})
	analyst, err := disabled.Build(providerconfig.EffectiveConfig{Enabled: true, BaseURL: "https://x", Model: "m"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := analyst.(analysis.RuleFallback); !ok {
		t.Fatalf("disabled external analysis built %T, want RuleFallback", analyst)
	}

	enabled := newProviderAnalystBuilder(config.Config{Security: config.SecurityConfig{AllowExternalLLMAnalysis: true}, Analysis: config.AnalysisConfig{MaxTokens: 128}})
	analyst, err = enabled.Build(providerconfig.EffectiveConfig{Enabled: true, BaseURL: "https://x", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("Build(enabled) error = %v", err)
	}
	if _, ok := analyst.(analysis.ResilientService); !ok {
		t.Fatalf("enabled provider built %T, want ResilientService", analyst)
	}

	// A disabled provider always degrades, even with external analysis allowed.
	analyst, err = enabled.Build(providerconfig.EffectiveConfig{Enabled: false})
	if err != nil {
		t.Fatalf("Build(disabled) error = %v", err)
	}
	if _, ok := analyst.(analysis.RuleFallback); !ok {
		t.Fatalf("disabled provider built %T, want RuleFallback", analyst)
	}

	// Probe reports a readable failure when external analysis is disabled.
	result, err := disabled.Probe(context.Background(), providerconfig.EffectiveConfig{Enabled: true, BaseURL: "https://x", Model: "m"})
	if err != nil || result.OK {
		t.Fatalf("Probe() = %+v err=%v, want a non-ok result", result, err)
	}
}

// TestRouterDispatchesPerTenant proves the router keeps tenant analysts
// separate and falls back to the shared analyst for tenants without one.
func TestRouterDispatchesPerTenant(t *testing.T) {
	router := analysis.NewRouter(analysis.RuleFallback{})
	called := ""
	router.Set("tenant-a", stubAnalyst{name: "a", onCall: func() { called = "a" }})
	batch := func(tenant string) ports.AnalysisBatch {
		return ports.AnalysisBatch{TenantID: tenant, RunID: "r", PromptVersion: "p", SchemaVersion: "s"}
	}
	if _, err := router.Analyze(context.Background(), batch("tenant-a")); err != nil {
		t.Fatalf("Analyze(a) error = %v", err)
	}
	if called != "a" {
		t.Fatalf("router did not dispatch to tenant-a analyst")
	}
	called = ""
	if _, err := router.Analyze(context.Background(), batch("tenant-b")); err != nil {
		t.Fatalf("Analyze(b) error = %v", err)
	}
	if called != "" {
		t.Fatalf("router dispatched tenant-b to tenant-a's analyst")
	}
}

type stubAnalyst struct {
	name   string
	onCall func()
}

func (s stubAnalyst) Analyze(context.Context, ports.AnalysisBatch) (ports.AnalystResult, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return ports.AnalystResult{}, nil
}

// TestProviderServiceRejectsUnusableMasterKey proves a configured-but-invalid
// master key fails fast instead of silently disabling provider secrets (every
// save would otherwise fail later with "encryption is not configured").
func TestProviderServiceRejectsUnusableMasterKey(t *testing.T) {
	cfg := config.Config{}
	cfg.MasterKey = "too-short"
	if _, err := NewProviderService(cfg, nil, providerAnalystBuilderStub{}); err == nil {
		t.Fatal("NewProviderService accepted an unusable master key")
	}
	cfg.MasterKey = "mengpo-dev-master-key--32bytes--"
	service, err := NewProviderService(cfg, nil, providerAnalystBuilderStub{})
	if err != nil {
		t.Fatalf("NewProviderService() error = %v", err)
	}
	if service.Sealer == nil {
		t.Fatal("sealer was not configured for a valid master key")
	}
}

type providerAnalystBuilderStub struct{}

func (providerAnalystBuilderStub) Build(providerconfig.EffectiveConfig) (ports.MemoryAnalyst, error) {
	return analysis.RuleFallback{}, nil
}

func (providerAnalystBuilderStub) Probe(context.Context, providerconfig.EffectiveConfig) (providerconfig.ProbeResult, error) {
	return providerconfig.ProbeResult{}, nil
}
