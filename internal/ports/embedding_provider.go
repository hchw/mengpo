package ports

import (
	"context"
	"errors"
)

var (
	ErrInvalidEmbeddingInput  = errors.New("embedding input must not be empty")
	ErrInvalidEmbeddingOutput = errors.New("embedding provider returned an invalid vector")
)

type EmbeddingMetadata struct {
	ModelID    string
	Artifact   string
	Version    string
	Dimensions int
}

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
	Metadata() EmbeddingMetadata
}
