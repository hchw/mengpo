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

// QueryEmbedder is an optional Embedder capability. When implemented, Recall
// uses EmbedQuery so retrieval queries carry the model's query task tag rather
// than the passage tag used for stored memory content.
type QueryEmbedder interface {
	EmbedQuery(context.Context, string) ([]float32, error)
}
