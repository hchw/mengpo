package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/config"
)

func TestParseVectorContract(t *testing.T) {
	values := make([]float32, ExpectedDimensions)
	for i := range values {
		values[i] = .01
	}
	encoded, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"embedding": values}}})
	got, err := parseVector(encoded)
	if err != nil || len(got) != ExpectedDimensions {
		t.Fatalf("vector dimension=%d err=%v", len(got), err)
	}
	if _, err := parseVector([]byte(`not-json`)); err == nil {
		t.Fatal("malformed output accepted")
	}
}

func TestLlamaCPPLoadsConfiguredArtifactAndEmbeds(t *testing.T) {
	binary, err := exec.LookPath("llama-embedding")
	if err != nil {
		t.Skip("llama-embedding is not installed")
	}
	artifact := filepath.Join("..", "..", "..", "models", "bge-small-zh-v1.5-Q8_0.gguf")
	if _, err := os.Stat(artifact); err != nil {
		t.Skip("repository GGUF artifact not available")
	}
	adapter, err := NewLlamaCPP(config.EmbeddingConfig{ModelID: ExpectedModelID, Artifact: artifact, Binary: binary, Dimensions: ExpectedDimensions})
	if err != nil {
		t.Fatal(err)
	}
	metadata := adapter.Metadata()
	if metadata.ModelID != ExpectedModelID || metadata.Dimensions != ExpectedDimensions || !strings.HasSuffix(metadata.Artifact, "Q8_0.gguf") || len(metadata.Version) != 64 {
		t.Fatalf("metadata=%#v", metadata)
	}
	vector, err := adapter.Embed(context.Background(), "中文代码检索：Go 中的租户路由和向量数据库")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != ExpectedDimensions {
		t.Fatalf("dimensions=%d", len(vector))
	}
}

func TestLlamaCPPRejectsWrongModelIdentityAndMissingArtifact(t *testing.T) {
	cfg := config.EmbeddingConfig{ModelID: "wrong-model", Artifact: "missing.gguf", Dimensions: ExpectedDimensions}
	if _, err := NewLlamaCPP(cfg); err != ErrInvalidArtifact {
		t.Fatalf("wrong model error=%v", err)
	}
	cfg.ModelID = ExpectedModelID
	if _, err := NewLlamaCPP(cfg); err == nil {
		t.Fatal("missing model file accepted")
	}
}

func TestEmbedRejectsInvalidInputAndZeroVector(t *testing.T) {
	if _, err := parseVector([]byte(`{"data":[{"embedding":[0,0]}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := parseVector([]byte(`{"data":[]}`)); err == nil {
		t.Fatal("empty response accepted")
	}
}

func TestLlamaCPPAppliesQueryAndDocumentPrefixes(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "llama-embedding")
	script := "#!/bin/sh\n" +
		"printf '%s' \"$*\" > \"" + argsFile + "\"\n" +
		"printf '{\"data\":[{\"embedding\":['\n" +
		fmt.Sprintf("i=1; while [ $i -le %d ]; do printf '0.1'; [ $i -lt %d ] && printf ','; i=$((i+1)); done\n", ExpectedDimensions, ExpectedDimensions) +
		"printf ']}]}'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "fake.gguf")
	if err := os.WriteFile(artifact, append([]byte("GGUF"), make([]byte, 24)...), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter, err := NewLlamaCPP(config.EmbeddingConfig{
		ModelID:        ExpectedModelID,
		Artifact:       artifact,
		Binary:         "llama-embedding",
		Dimensions:     ExpectedDimensions,
		QueryPrefix:    "query: ",
		DocumentPrefix: "passage: ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Embed(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if doc := readArgs(t, argsFile); !strings.Contains(doc, "-p passage: hello") {
		t.Fatalf("document embedding missing passage prefix: %q", doc)
	}
	if _, err := adapter.EmbedQuery(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if q := readArgs(t, argsFile); !strings.Contains(q, "-p query: hello") {
		t.Fatalf("query embedding missing query prefix: %q", q)
	}
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
