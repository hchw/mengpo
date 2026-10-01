package config

import "testing"

func TestDevSeedDefaults(t *testing.T) {
	base := map[string]string{
		"MEMORY_HTTP_ADDR":    ":8080",
		"MEMORY_DATABASE_URL": "postgres://example",
	}
	load := func(extra map[string]string) Config {
		t.Helper()
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		cfg, err := LoadFrom(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return cfg
	}

	if !load(nil).DevSeed {
		t.Fatal("DevSeed must default to true in development")
	}
	if load(map[string]string{"MEMORY_APP_ENV": "production"}).DevSeed {
		t.Fatal("DevSeed must default to false in production")
	}
	if !load(map[string]string{"MEMORY_DEV_SEED": "true", "MEMORY_APP_ENV": "production"}).DevSeed {
		t.Fatal("explicit MEMORY_DEV_SEED=true must override the production default")
	}
}
