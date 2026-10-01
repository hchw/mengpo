package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", ".."}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func canonicalSet() map[string]bool {
	set := make(map[string]bool)
	for _, names := range EnvNames() {
		set[names[0]] = true
	}
	return set
}

// TestEnvNamesAreNamespaced guards the canonical prefix so deploy manifests and
// docs never drift back to unprefixed names.
func TestEnvNamesAreNamespaced(t *testing.T) {
	for key, names := range EnvNames() {
		if len(names) == 0 {
			t.Fatalf("env key %q has no names", key)
		}
		if !strings.HasPrefix(names[0], "MEMORY_") {
			t.Fatalf("canonical name for %q = %q, want MEMORY_ prefix", key, names[0])
		}
	}
}

// TestCanonicalNamesTakePrecedence ensures the MEMORY_ alias wins over the
// legacy unprefixed variable when both are present.
func TestCanonicalNamesTakePrecedence(t *testing.T) {
	cfg, err := LoadFrom(func(key string) (string, bool) {
		switch key {
		case "MEMORY_DATABASE_URL":
			return "postgres://canonical/db", true
		case "DATABASE_URL":
			return "postgres://legacy/db", true
		case "MEMORY_HTTP_ADDR":
			return ":9000", true
		case "HTTP_ADDR":
			return ":1234", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://canonical/db" {
		t.Fatalf("DatabaseURL = %q, want canonical value", cfg.DatabaseURL)
	}
	if cfg.HTTPAddr != ":9000" {
		t.Fatalf("HTTPAddr = %q, want canonical value", cfg.HTTPAddr)
	}
}

// TestLegacyAliasesStillLoad keeps existing deployments working during the
// migration window.
func TestLegacyAliasesStillLoad(t *testing.T) {
	cfg, err := LoadFrom(func(key string) (string, bool) {
		switch key {
		case "DATABASE_URL":
			return "postgres://legacy/db", true
		case "MQ_ADAPTER":
			return "none", true
		case "EMBEDDING_ARTIFACT":
			return "legacy.gguf", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Queue.Adapter != "none" || cfg.Providers.Embedding.Artifact != "legacy.gguf" {
		t.Fatalf("legacy aliases not honored: %#v", cfg)
	}
}

// TestDotEnvExampleDocumentsCanonicalNames keeps .env.example complete.
func TestDotEnvExampleDocumentsCanonicalNames(t *testing.T) {
	content := repoFile(t, ".env.example")
	canonical := canonicalSet()
	for name := range canonical {
		if !strings.Contains(content, name+"=") {
			t.Errorf(".env.example does not document canonical variable %s", name)
		}
	}
}

func extractEnvNames(content string) []string {
	re := regexp.MustCompile(`MEMORY_[A-Z0-9_]+`)
	return re.FindAllString(content, -1)
}

// TestDeployManifestsUseCanonicalNames rejects any MEMORY_ variable that the
// configuration layer does not recognize.
func TestDeployManifestsUseCanonicalNames(t *testing.T) {
	canonical := canonicalSet()
	files := map[string]string{
		"deploy/dev/docker-compose.yml": repoFile(t, "deploy", "dev", "docker-compose.yml"),
		"docs/operations.md":            repoFile(t, "docs", "operations.md"),
	}
	allowedExtra := map[string]bool{
		"MEMORY_TEST_DATABASE_URL": true,
		"MEMORY_TEST_REDIS_URL":    true,
	}
	for path, content := range files {
		for _, name := range extractEnvNames(content) {
			if canonical[name] || allowedExtra[name] {
				continue
			}
			t.Errorf("%s references unknown configuration variable %s", path, name)
		}
	}
}
