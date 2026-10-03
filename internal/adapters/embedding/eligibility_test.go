package embedding

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/config"
)

func TestEmbeddingEligibilityChineseAndCodeRetrieval(t *testing.T) {
	binary, err := exec.LookPath("llama-embedding")
	if err != nil {
		t.Skip("llama-embedding is not installed")
	}
	artifact := filepath.Join("..", "..", "..", "models", "bge-small-zh-v1.5-Q8_0.gguf")
	adapter, err := NewLlamaCPP(config.EmbeddingConfig{ModelID: ExpectedModelID, Artifact: artifact, Binary: binary, Dimensions: ExpectedDimensions, Pooling: config.DefaultEmbeddingPooling})
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{"租户schema隔离", "租户的数据通过独立schema路由", "PostgreSQL outbox lease retry"}
	vectors := make([][]float32, len(queries))
	for i, q := range queries {
		vectors[i], err = adapter.EmbedQuery(context.Background(), q)
		if err != nil {
			t.Fatalf("embed %q: %v", q, err)
		}
	}
	for i, v := range vectors {
		if len(v) != ExpectedDimensions {
			t.Fatalf("query %d dimension=%d", i, len(v))
		}
		norm := 0.0
		for _, component := range v {
			if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
				t.Fatalf("query %d produced non-finite component", i)
			}
			norm += float64(component * component)
		}
		if math.Abs(math.Sqrt(norm)-1) > .02 {
			t.Fatalf("query %d normalized norm=%f", i, math.Sqrt(norm))
		}
	}
	// Eligibility smoke benchmark: a Chinese paraphrase about tenant/schema routing
	// must score meaningfully closer than a distinct PostgreSQL outbox concept.
	// bge models are anisotropic, so absolute cosine values are not informative;
	// the margin between the paraphrase and the unrelated query is. This is only a
	// local sanity sample, not the larger labeled evaluation for task 6.3.
	paraphrase := cosine(vectors[0], vectors[1])
	unrelated := cosine(vectors[0], vectors[2])
	if paraphrase <= unrelated+0.1 {
		t.Fatalf("Chinese paraphrase not separable: paraphrase=%f unrelated=%f", paraphrase, unrelated)
	}
	if !strings.Contains(adapter.Metadata().Artifact, "Q8_0") {
		t.Fatal("artifact quantization identity missing from metadata")
	}
}

func cosine(a, b []float32) float64 {
	dot, aa, bb := 0.0, 0.0, 0.0
	for i := range a {
		dot += float64(a[i] * b[i])
		aa += float64(a[i] * a[i])
		bb += float64(b[i] * b[i])
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / math.Sqrt(aa*bb)
}
