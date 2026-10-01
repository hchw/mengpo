package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/ports"
)

const ExpectedModelID = config.DefaultEmbeddingModel
const ExpectedDimensions = config.DefaultEmbeddingDim

var ErrInvalidArtifact = errors.New("embedding GGUF artifact is invalid or does not match configured model")

type LlamaCPP struct {
	binary     string
	artifact   string
	modelID    string
	dimensions int
	sha256     string
}

func NewLlamaCPP(cfg config.EmbeddingConfig) (*LlamaCPP, error) {
	if cfg.ModelID != ExpectedModelID || cfg.Dimensions != ExpectedDimensions || cfg.Artifact == "" {
		return nil, ErrInvalidArtifact
	}
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "llama-embedding"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("find llama.cpp embedding executable: %w", err)
	}
	file, err := os.Open(cfg.Artifact)
	if err != nil {
		return nil, fmt.Errorf("open embedding artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 24 {
		return nil, ErrInvalidArtifact
	}
	magic := make([]byte, 4)
	if _, err := file.ReadAt(magic, 0); err != nil || string(magic) != "GGUF" {
		return nil, ErrInvalidArtifact
	}
	hash := sha256.New()
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	return &LlamaCPP{binary: resolved, artifact: cfg.Artifact, modelID: cfg.ModelID, dimensions: cfg.Dimensions, sha256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (a *LlamaCPP) Metadata() ports.EmbeddingMetadata {
	return ports.EmbeddingMetadata{ModelID: a.modelID, Artifact: filepath.Base(a.artifact), Version: a.sha256, Dimensions: a.dimensions}
}

func (a *LlamaCPP) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, ports.ErrInvalidEmbeddingInput
	}
	cmd := exec.CommandContext(ctx, a.binary, "-m", a.artifact, "-p", text, "--embd-output-format", "json", "--pooling", "mean", "--embd-normalize", "2", "-ngl", "0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run llama.cpp embedding: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	vector, err := parseVector(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	if len(vector) != a.dimensions {
		return nil, fmt.Errorf("embedding output dimension %d, want %d", len(vector), a.dimensions)
	}
	norm := 0.0
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, ports.ErrInvalidEmbeddingOutput
		}
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return nil, ports.ErrInvalidEmbeddingOutput
	}
	return vector, nil
}

func parseVector(output []byte) ([]float32, error) {
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &response); err != nil || len(response.Data) != 1 || len(response.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("%w: malformed llama.cpp JSON output", ports.ErrInvalidEmbeddingOutput)
	}
	return response.Data[0].Embedding, nil
}
