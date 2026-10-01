package providerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/platform/providercrypto"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeStore struct {
	records map[string]ports.ProviderConfigRecord
}

func (f *fakeStore) key(tenant, provider string) string { return tenant + "|" + provider }

func (f *fakeStore) Get(_ context.Context, tenantID, provider string) (ports.ProviderConfigRecord, bool, error) {
	if f.records == nil {
		return ports.ProviderConfigRecord{}, false, nil
	}
	record, ok := f.records[f.key(tenantID, provider)]
	return record, ok, nil
}

func (f *fakeStore) Upsert(_ context.Context, tenantID string, record ports.ProviderConfigRecord) error {
	if f.records == nil {
		f.records = map[string]ports.ProviderConfigRecord{}
	}
	f.records[f.key(tenantID, record.Provider)] = record
	return nil
}

func (f *fakeStore) Delete(_ context.Context, tenantID, provider string) error {
	delete(f.records, f.key(tenantID, provider))
	return nil
}

type fakeBuilder struct {
	built    []EffectiveConfig
	failWith error
	probed   int
}

func (b *fakeBuilder) Build(cfg EffectiveConfig) (ports.MemoryAnalyst, error) {
	if b.failWith != nil {
		return nil, b.failWith
	}
	b.built = append(b.built, cfg)
	return analysis.RuleFallback{}, nil
}

func (b *fakeBuilder) Probe(context.Context, EffectiveConfig) (ProbeResult, error) {
	b.probed++
	return ProbeResult{OK: true, Latency: time.Millisecond}, nil
}

func newService(t *testing.T, store *fakeStore, builder *fakeBuilder, defaults EffectiveConfig) (*Service, *analysis.Router) {
	t.Helper()
	sealer, err := providercrypto.New("test-master-key-0123456789abcdef")
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	router := analysis.NewRouter(analysis.RuleFallback{})
	return &Service{Store: store, Sealer: sealer, Router: router, Builder: builder, Defaults: defaults, Provider: "memory-llm"}, router
}

func TestEffectivePrecedence(t *testing.T) {
	store := &fakeStore{}
	service, _ := newService(t, store, &fakeBuilder{}, EffectiveConfig{Enabled: true, BaseURL: "https://env.example", Model: "env-model"})

	// No tenant record, env default present -> env.
	cfg, err := service.Effective(context.Background(), "tenant-a")
	if err != nil || cfg.Source != SourceEnv || cfg.BaseURL != "https://env.example" {
		t.Fatalf("env default resolution = %+v err=%v", cfg, err)
	}

	// Tenant record wins.
	if _, err := service.Set(context.Background(), "tenant-a", "admin-1", Input{Enabled: true, BaseURL: "https://tenant.example", Model: "tenant-model", APIKey: "sk-tenant"}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	cfg, err = service.Effective(context.Background(), "tenant-a")
	if err != nil || cfg.Source != SourceTenant || cfg.BaseURL != "https://tenant.example" || cfg.APIKey != "sk-tenant" {
		t.Fatalf("tenant resolution = %+v err=%v", cfg, err)
	}

	// Another tenant still uses the env default.
	other, err := service.Effective(context.Background(), "tenant-b")
	if err != nil || other.Source != SourceEnv {
		t.Fatalf("tenant-b resolution = %+v err=%v", other, err)
	}

	// No tenant record and no env default -> disabled.
	disabledStore := &fakeStore{}
	disabled, _ := newService(t, disabledStore, &fakeBuilder{}, EffectiveConfig{})
	cfg, err = disabled.Effective(context.Background(), "tenant-c")
	if err != nil || cfg.Source != SourceDisabled || cfg.Enabled {
		t.Fatalf("disabled resolution = %+v err=%v", cfg, err)
	}
}

func TestSetHotSwapsAndNeverReturnsSecret(t *testing.T) {
	store := &fakeStore{}
	builder := &fakeBuilder{}
	service, router := newService(t, store, builder, EffectiveConfig{})

	view, err := service.Set(context.Background(), "tenant-a", "admin-1", Input{Enabled: true, BaseURL: "https://tenant.example", Model: "m", APIKey: "sk-secret-value"})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !view.HasSecret || view.SecretHint == "" || view.SecretHint == "sk-secret-value" {
		t.Fatalf("view secret handling = %+v", view)
	}
	if got := len(builder.built); got != 2 {
		// once for validation, once for apply
		t.Fatalf("Build calls = %d, want 2", got)
	}
	if !router.HasTenant("tenant-a") {
		t.Fatal("Set did not hot-swap the tenant analyst")
	}
	// The stored record must be ciphertext, not the raw secret.
	stored := store.records["tenant-a|memory-llm"]
	if len(stored.SecretCiphertext) == 0 || string(stored.SecretCiphertext) == "sk-secret-value" {
		t.Fatalf("stored secret is not encrypted: %+v", stored)
	}
	encoded, _ := json.Marshal(view)
	if string(encoded) == "" {
		t.Fatal("unreachable")
	}
}

func TestSetRejectsInvalidWithoutHotSwap(t *testing.T) {
	store := &fakeStore{}
	builder := &fakeBuilder{}
	service, router := newService(t, store, builder, EffectiveConfig{})
	if _, err := service.Set(context.Background(), "tenant-a", "admin-1", Input{Enabled: true, BaseURL: "", Model: "m"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid Set error = %v, want ErrInvalidConfig", err)
	}
	if router.HasTenant("tenant-a") {
		t.Fatal("invalid Set hot-swapped the analyst")
	}
	if len(store.records) != 0 {
		t.Fatal("invalid Set persisted a record")
	}

	// A builder failure must also leave the running config untouched.
	failing := &fakeBuilder{failWith: errors.New("bad endpoint")}
	service2, router2 := newService(t, &fakeStore{}, failing, EffectiveConfig{})
	if _, err := service2.Set(context.Background(), "tenant-a", "admin-1", Input{Enabled: true, BaseURL: "https://x", Model: "m"}); err == nil {
		t.Fatal("expected builder failure to reject the configuration")
	}
	if router2.HasTenant("tenant-a") {
		t.Fatal("builder failure hot-swapped the analyst")
	}
}

func TestDisabledTenantDoesNotFallBackToEnv(t *testing.T) {
	store := &fakeStore{}
	service, router := newService(t, store, &fakeBuilder{}, EffectiveConfig{Enabled: true, BaseURL: "https://env", Model: "env"})
	if _, err := service.Set(context.Background(), "tenant-a", "admin-1", Input{Enabled: false}); err != nil {
		t.Fatalf("Set(disabled) error = %v", err)
	}
	if !router.HasTenant("tenant-a") {
		t.Fatal("disabled tenant must install a rules-only analyst to override the env default")
	}
}

func TestTestDoesNotPersist(t *testing.T) {
	store := &fakeStore{}
	builder := &fakeBuilder{}
	service, _ := newService(t, store, builder, EffectiveConfig{})
	result, err := service.Test(context.Background(), "tenant-a", Input{Enabled: true, BaseURL: "https://x", Model: "m"})
	if err != nil || !result.OK {
		t.Fatalf("Test() = %+v err=%v", result, err)
	}
	if len(store.records) != 0 || builder.probed != 1 {
		t.Fatalf("Test persisted state: records=%d probed=%d", len(store.records), builder.probed)
	}
}
