// Package providerconfig resolves and manages a tenant's model provider
// configuration. Precedence is: tenant-stored configuration, then the
// environment default, then disabled. Provider secrets are encrypted at rest
// and are never returned to callers.
package providerconfig

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/platform/providercrypto"
	"github.com/hchw/mengpo/internal/ports"
)

const (
	SourceTenant   = "tenant"
	SourceEnv      = "env"
	SourceDisabled = "disabled"
)

// EffectiveConfig is the resolved, plaintext configuration for a tenant. APIKey
// is only ever held in memory; it must not be logged or persisted.
type EffectiveConfig struct {
	Provider string
	Enabled  bool
	BaseURL  string
	Model    string
	APIKey   string
	Source   string
}

// AnalystBuilder turns an effective configuration into a runnable analyst and
// probes connectivity. It is provided by the assembly layer so this package
// never imports a provider adapter.
type AnalystBuilder interface {
	Build(cfg EffectiveConfig) (ports.MemoryAnalyst, error)
	Probe(ctx context.Context, cfg EffectiveConfig) (ProbeResult, error)
}

// ProbeResult is the outcome of a connectivity test.
type ProbeResult struct {
	OK      bool
	Latency time.Duration
	Error   string
}

// View is the caller-facing configuration. It never contains the secret.
type View struct {
	Provider   string
	Enabled    bool
	BaseURL    string
	Model      string
	HasSecret  bool
	SecretHint string
	Source     string
	UpdatedBy  string
	UpdatedAt  time.Time
}

// Input is a configuration change request. APIKey is write-only: an empty value
// keeps the previously stored secret.
type Input struct {
	Enabled bool
	BaseURL string
	Model   string
	APIKey  string
}

// Service manages provider configuration for the "memory-llm" provider.
type Service struct {
	Store    ports.ProviderConfigStore
	Sealer   *providercrypto.Sealer
	Router   *analysis.Router
	Builder  AnalystBuilder
	Defaults EffectiveConfig
	Provider string
	Now      func() time.Time
	// Dynamic, when set, is invalidated on every change so cross-process
	// workers reload the tenant's provider without a restart.
	Dynamic *Dynamic
}

var (
	ErrInvalidConfig  = InvalidConfigError{}
	ErrSealerMissing  = errors.New("provider secret encryption is not configured")
	ErrBuilderMissing = errors.New("provider analyst builder is not configured")
)

// InvalidConfigError is the typed form of ErrInvalidConfig. It carries an API
// error code so a rejected provider configuration answers 400, while genuine
// server-side misconfiguration (a missing sealer or builder) still answers 500.
type InvalidConfigError struct{}

func (InvalidConfigError) Error() string        { return "provider configuration is invalid" }
func (InvalidConfigError) APIErrorCode() string { return "INVALID_CONFIG" }
func (InvalidConfigError) Retryable() bool      { return false }

// Effective resolves the configuration for a tenant.
func (s *Service) Effective(ctx context.Context, tenantID string) (EffectiveConfig, error) {
	provider := s.providerName()
	record, found, err := s.Store.Get(ctx, tenantID, provider)
	if err != nil {
		return EffectiveConfig{}, err
	}
	if found {
		cfg := EffectiveConfig{Provider: provider, Enabled: record.Enabled, BaseURL: record.BaseURL, Model: record.Model, Source: SourceTenant}
		if len(record.SecretCiphertext) > 0 {
			if s.Sealer == nil {
				return EffectiveConfig{}, ErrSealerMissing
			}
			secret, err := s.Sealer.Open(record.SecretCiphertext)
			if err != nil {
				return EffectiveConfig{}, err
			}
			cfg.APIKey = string(secret)
		}
		return cfg, nil
	}
	defaults := s.Defaults
	defaults.Provider = provider
	if defaults.Enabled && (defaults.BaseURL != "" || defaults.Model != "") {
		defaults.Source = SourceEnv
		return defaults, nil
	}
	return EffectiveConfig{Provider: provider, Source: SourceDisabled}, nil
}

// Get returns the caller-facing view, including whether a secret is stored.
func (s *Service) Get(ctx context.Context, tenantID string) (View, error) {
	provider := s.providerName()
	record, found, err := s.Store.Get(ctx, tenantID, provider)
	if err != nil {
		return View{}, err
	}
	if !found {
		effective, err := s.Effective(ctx, tenantID)
		if err != nil {
			return View{}, err
		}
		return View{Provider: provider, Enabled: effective.Enabled, BaseURL: effective.BaseURL, Model: effective.Model, HasSecret: effective.APIKey != "", Source: effective.Source}, nil
	}
	view := View{
		Provider:  provider,
		Enabled:   record.Enabled,
		BaseURL:   record.BaseURL,
		Model:     record.Model,
		HasSecret: len(record.SecretCiphertext) > 0,
		Source:    SourceTenant,
		UpdatedBy: record.UpdatedBy,
		UpdatedAt: record.UpdatedAt,
	}
	if view.HasSecret && s.Sealer != nil {
		if secret, err := s.Sealer.Open(record.SecretCiphertext); err == nil {
			view.SecretHint = hint(string(secret))
		}
	}
	return view, nil
}

// Set validates, persists and hot-swaps a tenant's provider configuration. The
// analyst is built and validated before anything is written, so a bad
// configuration is rejected without disturbing the running one.
func (s *Service) Set(ctx context.Context, tenantID, actor string, input Input) (View, error) {
	if s.Store == nil || s.Builder == nil {
		return View{}, ErrBuilderMissing
	}
	provider := s.providerName()
	existing, found, err := s.Store.Get(ctx, tenantID, provider)
	if err != nil {
		return View{}, err
	}
	baseURL := strings.TrimSpace(input.BaseURL)
	model := strings.TrimSpace(input.Model)
	if input.Enabled && (baseURL == "" || model == "") {
		return View{}, ErrInvalidConfig
	}
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" && found && len(existing.SecretCiphertext) > 0 {
		if s.Sealer == nil {
			return View{}, ErrSealerMissing
		}
		if secret, err := s.Sealer.Open(existing.SecretCiphertext); err == nil {
			apiKey = string(secret)
		}
	}
	cfg := EffectiveConfig{Provider: provider, Enabled: input.Enabled, BaseURL: baseURL, Model: model, APIKey: apiKey, Source: SourceTenant}
	if s.Builder != nil {
		if _, err := s.Builder.Build(cfg); err != nil {
			return View{}, err
		}
	}
	var ciphertext []byte
	keyVersion := 0
	if apiKey != "" {
		if s.Sealer == nil {
			return View{}, ErrSealerMissing
		}
		ciphertext, err = s.Sealer.Seal([]byte(apiKey))
		if err != nil {
			return View{}, err
		}
		keyVersion = s.Sealer.KeyVersion()
	}
	record := ports.ProviderConfigRecord{
		Provider:         provider,
		Enabled:          input.Enabled,
		BaseURL:          baseURL,
		Model:            model,
		SecretCiphertext: ciphertext,
		KeyVersion:       keyVersion,
		UpdatedBy:        actor,
	}
	if err := s.Store.Upsert(ctx, tenantID, record); err != nil {
		return View{}, err
	}
	if err := s.apply(tenantID, cfg); err != nil {
		return View{}, err
	}
	return s.Get(ctx, tenantID)
}

// Delete removes a tenant's configuration so the environment default applies.
func (s *Service) Delete(ctx context.Context, tenantID string) error {
	provider := s.providerName()
	if err := s.Store.Delete(ctx, tenantID, provider); err != nil {
		return err
	}
	if s.Router != nil {
		s.Router.Clear(tenantID)
	}
	if s.Dynamic != nil {
		s.Dynamic.Invalidate(tenantID)
	}
	return nil
}

// Test probes connectivity without persisting anything.
func (s *Service) Test(ctx context.Context, tenantID string, input Input) (ProbeResult, error) {
	if s.Builder == nil {
		return ProbeResult{}, ErrBuilderMissing
	}
	cfg := EffectiveConfig{Provider: s.providerName(), Enabled: true, BaseURL: strings.TrimSpace(input.BaseURL), Model: strings.TrimSpace(input.Model), APIKey: strings.TrimSpace(input.APIKey), Source: SourceTenant}
	if cfg.BaseURL == "" || cfg.Model == "" {
		return ProbeResult{}, ErrInvalidConfig
	}
	if cfg.APIKey == "" {
		if existing, found, err := s.Store.Get(ctx, tenantID, s.providerName()); err == nil && found && len(existing.SecretCiphertext) > 0 && s.Sealer != nil {
			if secret, err := s.Sealer.Open(existing.SecretCiphertext); err == nil {
				cfg.APIKey = string(secret)
			}
		}
	}
	return s.Builder.Probe(ctx, cfg)
}

// apply installs the tenant analyst so it takes effect without a restart.
func (s *Service) apply(tenantID string, cfg EffectiveConfig) error {
	if s.Dynamic != nil {
		s.Dynamic.Invalidate(tenantID)
	}
	if s.Router == nil {
		return nil
	}
	if !cfg.Enabled {
		// A tenant that explicitly disables the provider must not fall back to
		// the environment default, so install a rules-only analyst.
		s.Router.Set(tenantID, analysis.RuleFallback{})
		return nil
	}
	analyst, err := s.Builder.Build(cfg)
	if err != nil {
		return err
	}
	s.Router.Set(tenantID, analyst)
	return nil
}

func (s *Service) providerName() string {
	if s.Provider != "" {
		return s.Provider
	}
	return "memory-llm"
}

func hint(secret string) string {
	if len(secret) <= 4 {
		return "****"
	}
	return "****" + secret[len(secret)-4:]
}
