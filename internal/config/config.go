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

// Config is the single source of runtime configuration. Canonical environment
// names use the MEMORY_ prefix and match deploy/dev/docker-compose.yml and
// docs/operations.md. Legacy unprefixed names remain accepted during the
// migration window so existing deployments keep working.
type Config struct {
	Environment       string
	HTTPAddr          string
	ReadHeaderTimeout time.Duration
	RequestTimeout    time.Duration
	DatabaseURL       string
	RedisURL          string
	MigrateOnStart    bool
	// DevSeed provisions a demo tenant and membership so the console is usable in development.
	// It defaults to true outside production and is ignored in production.
	DevSeed   bool
	Auth      AuthConfig
	Scopes    []string
	Providers ProviderConfig
	Queue     QueueConfig
	Security  SecurityConfig
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
	Binary     string
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

// envNames maps one canonical key to zero or more legacy aliases. The canonical
// key is always tried first.
func envNames(canonical string, legacy ...string) []string {
	return append([]string{canonical}, legacy...)
}

func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

func LoadFrom(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment:       envString(lookup, envNames("MEMORY_APP_ENV", "APP_ENV"), "development"),
		HTTPAddr:          envString(lookup, envNames("MEMORY_HTTP_ADDR", "HTTP_ADDR"), ":8080"),
		ReadHeaderTimeout: 5 * time.Second,
		RequestTimeout:    30 * time.Second,
		DatabaseURL:       strings.TrimSpace(envString(lookup, envNames("MEMORY_DATABASE_URL", "DATABASE_URL"), "")),
		RedisURL:          strings.TrimSpace(envString(lookup, envNames("MEMORY_REDIS_URL"), "")),
		MigrateOnStart:    true,
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
				BaseURL: envString(lookup, envNames("MEMORY_LLM_BASE_URL"), ""),
				Model:   envString(lookup, envNames("MEMORY_LLM_MODEL"), ""),
				APIKey:  envString(lookup, envNames("MEMORY_LLM_API_KEY"), ""),
			},
			Embedding: EmbeddingConfig{
				Enabled:    false,
				ModelID:    DefaultEmbeddingModel,
				Artifact:   envString(lookup, envNames("MEMORY_EMBEDDING_ARTIFACT", "EMBEDDING_ARTIFACT"), "models/all-MiniLM-L6-v2-Q8_0.gguf"),
				Binary:     envString(lookup, envNames("MEMORY_EMBEDDING_BINARY", "EMBEDDING_BINARY"), "llama-embedding"),
				Dimensions: DefaultEmbeddingDim,
			},
			Reranker: RerankerConfig{
				Enabled: false,
				BaseURL: envString(lookup, envNames("MEMORY_RERANKER_BASE_URL", "RERANKER_BASE_URL"), ""),
				Model:   envString(lookup, envNames("MEMORY_RERANKER_MODEL", "RERANKER_MODEL"), ""),
				APIKey:  envString(lookup, envNames("MEMORY_RERANKER_API_KEY", "RERANKER_API_KEY"), ""),
			},
		},
		Queue: QueueConfig{
			Adapter: envString(lookup, envNames("MEMORY_QUEUE_ADAPTER", "MQ_ADAPTER"), "nats-core"),
			URL:     envString(lookup, envNames("MEMORY_NATS_URL", "MQ_URL"), "nats://127.0.0.1:4222"),
		},
		Security: SecurityConfig{
			MaxRequestBytes: 1 << 20,
		},
	}

	var err error
	if cfg.Auth.SessionTTL, err = envDuration(lookup, envNames("MEMORY_AUTH_SESSION_TTL", "AUTH_SESSION_TTL"), cfg.Auth.SessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = envDuration(lookup, envNames("MEMORY_HTTP_REQUEST_TIMEOUT", "HTTP_REQUEST_TIMEOUT"), cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Auth.SessionCookieSecure, err = envBool(lookup, envNames("MEMORY_AUTH_COOKIE_SECURE", "AUTH_COOKIE_SECURE"), cfg.Environment == "production"); err != nil {
		return Config{}, err
	}
	if cfg.MigrateOnStart, err = envBool(lookup, envNames("MEMORY_MIGRATE_ON_START"), cfg.MigrateOnStart); err != nil {
		return Config{}, err
	}
	if cfg.DevSeed, err = envBool(lookup, envNames("MEMORY_DEV_SEED"), cfg.Environment == "development"); err != nil {
		return Config{}, err
	}
	if cfg.Providers.MemoryLLM.Enabled, err = envBool(lookup, envNames("MEMORY_LLM_ENABLED"), cfg.Providers.MemoryLLM.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.Providers.Embedding.Enabled, err = envBool(lookup, envNames("MEMORY_EMBEDDING_ENABLED", "EMBEDDING_ENABLED"), cfg.Providers.Embedding.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.Providers.Reranker.Enabled, err = envBool(lookup, envNames("MEMORY_RERANKER_ENABLED", "RERANKER_ENABLED"), cfg.Providers.Reranker.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.Security.AllowExternalLLMAnalysis, err = envBool(lookup, envNames("MEMORY_ALLOW_EXTERNAL_LLM_ANALYSIS", "ALLOW_EXTERNAL_LLM_ANALYSIS"), false); err != nil {
		return Config{}, err
	}
	if cfg.Security.MaxRequestBytes, err = envInt64(lookup, envNames("MEMORY_MAX_REQUEST_BYTES", "MAX_REQUEST_BYTES"), cfg.Security.MaxRequestBytes); err != nil {
		return Config{}, err
	}
	if cfg.Environment == "production" {
		cfg.Auth.SessionCookieSecure = true
		if value, ok := lookupFirst(lookup, envNames("MEMORY_AUTH_COOKIE_SECURE", "AUTH_COOKIE_SECURE")); ok {
			secure, parseErr := strconv.ParseBool(value)
			if parseErr != nil {
				return Config{}, fmt.Errorf("MEMORY_AUTH_COOKIE_SECURE: %w", parseErr)
			}
			if !secure {
				return Config{}, errors.New("MEMORY_AUTH_COOKIE_SECURE cannot be disabled in production")
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
		return fmt.Errorf("unsupported MEMORY_APP_ENV %q", c.Environment)
	}
	if c.HTTPAddr == "" || c.DatabaseURL == "" {
		return errors.New("MEMORY_HTTP_ADDR and MEMORY_DATABASE_URL are required")
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
		return errors.New("MEMORY_MAX_REQUEST_BYTES must be positive")
	}
	if c.Providers.Embedding.ModelID != DefaultEmbeddingModel || c.Providers.Embedding.Dimensions != DefaultEmbeddingDim {
		return errors.New("embedding model identity/dimensions do not match the configured initial model")
	}
	if c.Providers.MemoryLLM.Enabled && (c.Providers.MemoryLLM.BaseURL == "" || c.Providers.MemoryLLM.Model == "") {
		return errors.New("enabled Memory LLM requires MEMORY_LLM_BASE_URL and MEMORY_LLM_MODEL")
	}
	if c.Providers.Reranker.Enabled && (c.Providers.Reranker.BaseURL == "" || c.Providers.Reranker.Model == "") {
		return errors.New("enabled reranker requires MEMORY_RERANKER_BASE_URL and MEMORY_RERANKER_MODEL")
	}
	if c.Providers.Embedding.Enabled && c.Providers.Embedding.Artifact == "" {
		return errors.New("enabled embedding provider requires MEMORY_EMBEDDING_ARTIFACT")
	}
	if c.Queue.Adapter != "nats-core" && c.Queue.Adapter != "none" {
		return fmt.Errorf("unsupported MEMORY_QUEUE_ADAPTER/MQ_ADAPTER %q", c.Queue.Adapter)
	}
	if c.Queue.Adapter == "nats-core" && c.Queue.URL == "" {
		return errors.New("MEMORY_NATS_URL is required when MEMORY_QUEUE_ADAPTER=nats-core")
	}
	if c.Environment == "production" && !c.Auth.SessionCookieSecure {
		return errors.New("secure auth cookies are required in production")
	}
	return nil
}

// EnvNames exposes the canonical and legacy environment variable names for the
// configuration surface. It is the machine-readable contract used by the
// consistency test that keeps deployment manifests and docs aligned.
func EnvNames() map[string][]string {
	return map[string][]string{
		"app_env":               envNames("MEMORY_APP_ENV", "APP_ENV"),
		"http_addr":             envNames("MEMORY_HTTP_ADDR", "HTTP_ADDR"),
		"http_request_timeout":  envNames("MEMORY_HTTP_REQUEST_TIMEOUT", "HTTP_REQUEST_TIMEOUT"),
		"database_url":          envNames("MEMORY_DATABASE_URL", "DATABASE_URL"),
		"redis_url":             envNames("MEMORY_REDIS_URL"),
		"migrate_on_start":      envNames("MEMORY_MIGRATE_ON_START"),
		"auth_session_ttl":      envNames("MEMORY_AUTH_SESSION_TTL", "AUTH_SESSION_TTL"),
		"auth_cookie_secure":    envNames("MEMORY_AUTH_COOKIE_SECURE", "AUTH_COOKIE_SECURE"),
		"llm_enabled":           envNames("MEMORY_LLM_ENABLED"),
		"llm_base_url":          envNames("MEMORY_LLM_BASE_URL"),
		"llm_model":             envNames("MEMORY_LLM_MODEL"),
		"llm_api_key":           envNames("MEMORY_LLM_API_KEY"),
		"embedding_enabled":     envNames("MEMORY_EMBEDDING_ENABLED", "EMBEDDING_ENABLED"),
		"embedding_artifact":    envNames("MEMORY_EMBEDDING_ARTIFACT", "EMBEDDING_ARTIFACT"),
		"embedding_binary":      envNames("MEMORY_EMBEDDING_BINARY", "EMBEDDING_BINARY"),
		"reranker_enabled":      envNames("MEMORY_RERANKER_ENABLED", "RERANKER_ENABLED"),
		"reranker_base_url":     envNames("MEMORY_RERANKER_BASE_URL", "RERANKER_BASE_URL"),
		"reranker_model":        envNames("MEMORY_RERANKER_MODEL", "RERANKER_MODEL"),
		"reranker_api_key":      envNames("MEMORY_RERANKER_API_KEY", "RERANKER_API_KEY"),
		"queue_adapter":         envNames("MEMORY_QUEUE_ADAPTER", "MQ_ADAPTER"),
		"queue_url":             envNames("MEMORY_NATS_URL", "MQ_URL"),
		"max_request_bytes":     envNames("MEMORY_MAX_REQUEST_BYTES", "MAX_REQUEST_BYTES"),
		"external_llm_analysis": envNames("MEMORY_ALLOW_EXTERNAL_LLM_ANALYSIS", "ALLOW_EXTERNAL_LLM_ANALYSIS"),
	}
}

func lookupFirst(lookup LookupEnv, keys []string) (string, bool) {
	for _, key := range keys {
		if value, ok := lookup(key); ok {
			return value, true
		}
	}
	return "", false
}

func envString(lookup LookupEnv, keys []string, fallback string) string {
	if value, ok := lookupFirst(lookup, keys); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envDuration(lookup LookupEnv, keys []string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookupFirst(lookup, keys)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", keys[0], err)
	}
	return parsed, nil
}

func envBool(lookup LookupEnv, keys []string, fallback bool) (bool, error) {
	value, ok := lookupFirst(lookup, keys)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s: %w", keys[0], err)
	}
	return parsed, nil
}

func envInt64(lookup LookupEnv, keys []string, fallback int64) (int64, error) {
	value, ok := lookupFirst(lookup, keys)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", keys[0], err)
	}
	return parsed, nil
}
