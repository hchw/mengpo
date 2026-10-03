package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hchw/mengpo/internal/adapters/llm"
	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/application/providerconfig"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/ports"
)

// providerAnalystBuilder builds a runnable analyst (and probes connectivity)
// from a resolved provider configuration. It keeps provider adapters out of the
// application layer.
type providerAnalystBuilder struct {
	allowExternal bool
	maxTokens     int
	timeout       time.Duration
	maxAttempts   int
}

func newProviderAnalystBuilder(cfg config.Config) providerAnalystBuilder {
	return providerAnalystBuilder{
		allowExternal: cfg.Security.AllowExternalLLMAnalysis,
		maxTokens:     cfg.Analysis.MaxTokens,
		timeout:       cfg.Providers.MemoryLLM.Timeout,
		maxAttempts:   cfg.Providers.MemoryLLM.MaxAttempts,
	}
}

// Build returns the analyst for an effective configuration. A disabled provider
// or disabled external analysis degrades to the deterministic rule baseline;
// the dispatcher separately captures explicit instructions and session
// summaries when no candidate is produced.
func (b providerAnalystBuilder) Build(cfg providerconfig.EffectiveConfig) (ports.MemoryAnalyst, error) {
	if !cfg.Enabled || !b.allowExternal {
		return analysis.RuleFallback{}, nil
	}
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, providerconfig.ErrInvalidConfig
	}
	adapter, err := llm.New(llm.Options{
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		APIKey:        cfg.APIKey,
		Timeout:       b.timeout,
		MaxAttempts:   b.maxAttempts,
		MaxTokens:     b.maxTokens,
		AllowExternal: true,
	})
	if err != nil {
		return nil, err
	}
	return analysis.ResilientService{Primary: adapter, Fallback: analysis.RuleFallback{}}, nil
}

// Probe performs a minimal connectivity check. It never persists anything.
func (b providerAnalystBuilder) Probe(ctx context.Context, cfg providerconfig.EffectiveConfig) (providerconfig.ProbeResult, error) {
	if !b.allowExternal {
		return providerconfig.ProbeResult{OK: false, Error: "external llm analysis is disabled"}, nil
	}
	adapter, err := llm.New(llm.Options{
		BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey,
		Timeout: b.timeout, MaxAttempts: 1, MaxTokens: 16, AllowExternal: true,
	})
	if err != nil {
		return providerconfig.ProbeResult{}, err
	}
	started := time.Now()
	_, err = adapter.Analyze(ctx, ports.AnalysisBatch{
		TenantID:      "probe",
		RunID:         "probe",
		TaskType:      "classify_event",
		PromptVersion: "probe",
		SchemaVersion: "probe",
		Events: []ports.AnalysisEvent{{
			EventID:    "probe",
			OccurredAt: time.Now().UTC(),
			Payload:    json.RawMessage(`{"text":"ping"}`),
		}},
	})
	latency := time.Since(started)
	if err != nil {
		if errors.Is(err, llm.ErrExternalAnalysisDisabled) {
			return providerconfig.ProbeResult{OK: false, Latency: latency, Error: "external llm analysis is disabled"}, nil
		}
		return providerconfig.ProbeResult{OK: false, Latency: latency, Error: err.Error()}, nil
	}
	return providerconfig.ProbeResult{OK: true, Latency: latency}, nil
}

var _ providerconfig.AnalystBuilder = providerAnalystBuilder{}
