package providerconfig

import (
	"context"
	"testing"

	"github.com/hchw/mengpo/internal/ports"
)

func TestDynamicResolvesPerTenantAndReloadsAfterChange(t *testing.T) {
	store := &fakeStore{}
	builder := &fakeBuilder{}
	service, _ := newService(t, store, builder, EffectiveConfig{})
	dynamic := &Dynamic{Service: service}
	service.Dynamic = dynamic

	batch := func(tenant string) ports.AnalysisBatch {
		return ports.AnalysisBatch{TenantID: tenant, RunID: "r", PromptVersion: "p", SchemaVersion: "s"}
	}
	// tenant-a has no config: resolves to the (disabled) default.
	if _, err := dynamic.Analyze(context.Background(), batch("tenant-a")); err != nil {
		t.Fatalf("Analyze(a) error = %v", err)
	}
	buildsAfterFirst := len(builder.built)

	// Saving tenant-a config invalidates the cache and reloads it.
	if _, err := service.Set(context.Background(), "tenant-a", "admin-1", Input{Enabled: true, BaseURL: "https://tenant-a.example", Model: "m", APIKey: "sk-a"}); err != nil {
		t.Fatalf("Set(a) error = %v", err)
	}
	if _, err := dynamic.Analyze(context.Background(), batch("tenant-a")); err != nil {
		t.Fatalf("Analyze(a) after Set error = %v", err)
	}
	if len(builder.built) == buildsAfterFirst {
		t.Fatal("dynamic did not reload after the configuration changed")
	}
	last := builder.built[len(builder.built)-1]
	if last.BaseURL != "https://tenant-a.example" || last.APIKey != "sk-a" {
		t.Fatalf("reloaded config = %+v", last)
	}

	// A different tenant still resolves independently.
	if _, err := dynamic.Analyze(context.Background(), batch("tenant-b")); err != nil {
		t.Fatalf("Analyze(b) error = %v", err)
	}
	other := builder.built[len(builder.built)-1]
	if other.BaseURL == "https://tenant-a.example" {
		t.Fatalf("tenant-b leaked tenant-a's provider: %+v", other)
	}
}
