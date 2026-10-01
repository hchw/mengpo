package assembly

import (
	"fmt"

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/providerconfig"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/platform/providercrypto"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
)

// NewProviderService assembles the per-tenant provider configuration service.
// The master key is required only to store secrets; without it, saving a secret
// is rejected rather than stored in the clear.
func NewProviderService(cfg config.Config, router *tenantdb.Router, builder providerconfig.AnalystBuilder) (*providerconfig.Service, error) {
	service := &providerconfig.Service{
		Store:    postgres.NewProviderConfigRepository(router),
		Router:   analysis.NewRouter(analysis.RuleFallback{}),
		Builder:  builder,
		Provider: "memory-llm",
		Defaults: providerconfig.EffectiveConfig{
			Provider: "memory-llm",
			Enabled:  cfg.Providers.MemoryLLM.Enabled,
			BaseURL:  cfg.Providers.MemoryLLM.BaseURL,
			Model:    cfg.Providers.MemoryLLM.Model,
			APIKey:   cfg.Providers.MemoryLLM.APIKey,
		},
	}
	if cfg.MasterKey != "" {
		sealer, err := providercrypto.New(cfg.MasterKey)
		if err != nil {
			// A configured-but-unusable key must not silently disable secret
			// storage: every provider save would fail at runtime instead.
			return nil, fmt.Errorf("provider secret master key is invalid: %w", err)
		}
		service.Sealer = sealer
	}
	return service, nil
}
