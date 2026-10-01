package embedding

import (
	"context"
	"encoding/json"
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
	artifact := filepath.Join("..", "..", "..", "models", "all-MiniLM-L6-v2-Q8_0.gguf")
	if _, err := os.Stat(artifact); err != nil {
		t.Skip("repository GGUF artifact not available")
	}
	adapter, err := NewLlamaCPP(config.EmbeddingConfig{ModelID: ExpectedModelID, Artifact: artifact, Binary: binary, Dimensions: ExpectedDimensions})
	if err != nil {
		t.Fatal(err)
	}
	metadata := adapter.Metadata()
	if metadata.ModelID != ExpectedModelID || metadata.Dimensions != 384 || !strings.HasSuffix(metadata.Artifact, "Q8_0.gguf") || len(metadata.Version) != 64 {
		t.Fatalf("metadata=%#v", metadata)
	}
	vector, err := adapter.Embed(context.Background(), "中文代码检索：Go 中的租户路由和向量数据库")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != 384 {
		t.Fatalf("dimensions=%d", len(vector))
	}
}

func TestLlamaCPPRejectsWrongModelIdentityAndMissingArtifact(t *testing.T) {
	cfg := config.EmbeddingConfig{ModelID: "wrong-model", Artifact: "missing.gguf", Dimensions: 384}
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
