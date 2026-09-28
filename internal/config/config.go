package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultEmbeddingModel = "all-MiniLM-L6-v2"
	DefaultEmbeddingDim   = 384
)

type LookupEnv func(string) (string, bool)

type Config struct {
	Environment       string
	HTTPAddr          string
	ReadHeaderTimeout time.Duration
	RequestTimeout    time.Duration
	DatabaseURL       string
	Auth              AuthConfig
	Scopes            []string
	Providers         ProviderConfig
	Queue             QueueConfig
	Security          SecurityConfig
}

type AuthConfig struct {
	SessionTTL              time.Duration
	SessionCookieName       string
	SessionCookieSecure     bool
	PasswordHashMemoryKiB   uint32
	PasswordHashIterations  uint32
	PasswordHashParallelism uint8
}

type ProviderConfig struct {
	MemoryLLM LLMConfig
	Embedding EmbeddingConfig
	Reranker  RerankerConfig
}

type LLMConfig struct {
	Enabled bool
	BaseURL string
	Model   string
	APIKey  string
}

type EmbeddingConfig struct {
	Enabled    bool
	ModelID    string
	Artifact   string
	Dimensions int
}

type RerankerConfig struct {
	Enabled bool
	BaseURL string
	Model   string
	APIKey  string
}

type QueueConfig struct {
	Adapter string
	URL     string
}

type SecurityConfig struct {
	MaxRequestBytes          int64
	AllowExternalLLMAnalysis bool
}

func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

func LoadFrom(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment:       envString(lookup, "APP_ENV", "development"),
		HTTPAddr:          envString(lookup, "HTTP_ADDR", ":8080"),
		ReadHeaderTimeout: 5 * time.Second,
		RequestTimeout:    30 * time.Second,
		DatabaseURL:       strings.TrimSpace(envString(lookup, "DATABASE_URL", "")),
		Auth: AuthConfig{
			SessionTTL:              12 * time.Hour,
			SessionCookieName:       "memory_session",
			PasswordHashMemoryKiB:   64 * 1024,
			PasswordHashIterations:  3,
			PasswordHashParallelism: 1,
		},
		Scopes: []string{"user-global", "session"},
		Providers: ProviderConfig{
			MemoryLLM: LLMConfig{
				Enabled: false,
				BaseURL: envString(lookup, "MEMORY_LLM_BASE_URL", ""),
				Model:   envString(lookup, "MEMORY_LLM_MODEL", ""),
				APIKey:  envString(lookup, "MEMORY_LLM_API_KEY", ""),
			},
			Embedding: EmbeddingConfig{
				Enabled:    false,
				ModelID:    DefaultEmbeddingModel,
				Artifact:   envString(lookup, "EMBEDDING_ARTIFACT", "models/all-MiniLM-L6-v2-Q8_0.gguf"),
				Dimensions: DefaultEmbeddingDim,
			},
			Reranker: RerankerConfig{
				Enabled: false,
				BaseURL: envString(lookup, "RERANKER_BASE_URL", ""),
				Model:   envString(lookup, "RERANKER_MODEL", ""),
				APIKey:  envString(lookup, "RERANKER_API_KEY", ""),
			},
		},
		Queue: QueueConfig{
			Adapter: envString(lookup, "MQ_ADAPTER", "nats-core"),
			URL:     envString(lookup, "MQ_URL", "nats://127.0.0.1:4222"),
		},
		Security: SecurityConfig{
			MaxRequestBytes: 1 << 20,
		},
	}

	var err error
	if cfg.Auth.SessionTTL, err = envDuration(lookup, "AUTH_SESSION_TTL", cfg.Auth.SessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = envDuration(lookup, "HTTP_REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Auth.SessionCookieSecure, err = envBool(lookup, "AUTH_COOKIE_SECURE", cfg.Environment == "production"); err != nil {
		return Config{}, err
	}
	if cfg.Providers.MemoryLLM.Enabled, err = envBool(lookup, "MEMORY_LLM_ENABLED", cfg.Providers.MemoryLLM.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.Providers.Embedding.Enabled, err = envBool(lookup, "EMBEDDING_ENABLED", cfg.Providers.Embedding.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.Providers.Reranker.Enabled, err = envBool(lookup, "RERANKER_ENABLED", cfg.Providers.Reranker.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.Security.AllowExternalLLMAnalysis, err = envBool(lookup, "ALLOW_EXTERNAL_LLM_ANALYSIS", false); err != nil {
		return Config{}, err
	}
	if cfg.Security.MaxRequestBytes, err = envInt64(lookup, "MAX_REQUEST_BYTES", cfg.Security.MaxRequestBytes); err != nil {
		return Config{}, err
	}
	if cfg.Environment == "production" {
		cfg.Auth.SessionCookieSecure = true
		if value, ok := lookup("AUTH_COOKIE_SECURE"); ok {
			secure, parseErr := strconv.ParseBool(value)
			if parseErr != nil {
				return Config{}, fmt.Errorf("AUTH_COOKIE_SECURE: %w", parseErr)
			}
			if !secure {
				return Config{}, errors.New("AUTH_COOKIE_SECURE cannot be disabled in production")
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "production" {
		return fmt.Errorf("unsupported APP_ENV %q", c.Environment)
	}
	if c.HTTPAddr == "" || c.DatabaseURL == "" {
		return errors.New("HTTP_ADDR and DATABASE_URL are required")
	}
	if c.ReadHeaderTimeout <= 0 || c.RequestTimeout <= 0 || c.Auth.SessionTTL <= 0 {
		return errors.New("HTTP timeouts and auth session TTL must be positive")
	}
	if c.Auth.SessionCookieName == "" || c.Auth.PasswordHashMemoryKiB < 8*1024 ||
		c.Auth.PasswordHashIterations == 0 || c.Auth.PasswordHashParallelism == 0 {
		return errors.New("auth security settings are invalid")
	}
	if len(c.Scopes) != 2 || c.Scopes[0] != "user-global" || c.Scopes[1] != "session" {
		return errors.New("only user-global and session memory scopes are supported")
	}
	if c.Security.MaxRequestBytes <= 0 {
		return errors.New("MAX_REQUEST_BYTES must be positive")
	}
	if c.Providers.Embedding.ModelID != DefaultEmbeddingModel || c.Providers.Embedding.Dimensions != DefaultEmbeddingDim {
		return errors.New("embedding model identity/dimensions do not match the configured initial model")
	}
	if c.Providers.MemoryLLM.Enabled && (c.Providers.MemoryLLM.BaseURL == "" || c.Providers.MemoryLLM.Model == "") {
		return errors.New("enabled Memory LLM requires MEMORY_LLM_BASE_URL and MEMORY_LLM_MODEL")
	}
	if c.Providers.Reranker.Enabled && (c.Providers.Reranker.BaseURL == "" || c.Providers.Reranker.Model == "") {
		return errors.New("enabled reranker requires RERANKER_BASE_URL and RERANKER_MODEL")
	}
	if c.Providers.Embedding.Enabled && c.Providers.Embedding.Artifact == "" {
		return errors.New("enabled embedding provider requires EMBEDDING_ARTIFACT")
	}
	if c.Queue.Adapter != "nats-core" && c.Queue.Adapter != "none" {
		return fmt.Errorf("unsupported MQ_ADAPTER %q", c.Queue.Adapter)
	}
	if c.Queue.Adapter == "nats-core" && c.Queue.URL == "" {
		return errors.New("MQ_URL is required when MQ_ADAPTER=nats-core")
	}
	if c.Environment == "production" && !c.Auth.SessionCookieSecure {
		return errors.New("secure auth cookies are required in production")
	}
	return nil
}

func envString(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envDuration(lookup LookupEnv, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func envBool(lookup LookupEnv, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func envInt64(lookup LookupEnv, key string, fallback int64) (int64, error) {
	value, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}
