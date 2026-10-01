package reranker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/ports"
)

const (
	MaxRerankDocuments   = 100
	maxResponseBytes     = 1 << 20
	defaultRerankTimeout = 10 * time.Second
)

var (
	ErrRerankerDisabled    = errors.New("reranker provider is not enabled")
	ErrRerankerBadResponse = errors.New("reranker provider returned a malformed response")
)

// HTTP is an external cross-encoder reranker adapter. The ranking model is a
// separate provider from the local embedding pipeline; it is reached over
// HTTP with its own base URL, model id and optional API key.
type HTTP struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

func NewHTTP(cfg config.RerankerConfig) (*HTTP, error) {
	if !cfg.Enabled {
		return nil, ErrRerankerDisabled
	}
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, ports.ErrInvalidRerankRequest
	}
	return &HTTP{
		baseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		model:   strings.TrimSpace(cfg.Model),
		apiKey:  strings.TrimSpace(cfg.APIKey),
		client:  &http.Client{Timeout: defaultRerankTimeout},
	}, nil
}

func (h *HTTP) Metadata() ports.RerankerMetadata {
	return ports.RerankerMetadata{Model: h.model, Version: "http"}
}

func (h *HTTP) Rank(ctx context.Context, query string, candidates []ports.RerankCandidate) ([]ports.RerankResult, error) {
	if strings.TrimSpace(query) == "" || len(candidates) == 0 || len(candidates) > MaxRerankDocuments {
		return nil, ports.ErrInvalidRerankRequest
	}
	documents := make([]string, len(candidates))
	ids := make([]string, len(candidates))
	for index, candidate := range candidates {
		if candidate.ID == "" || strings.TrimSpace(candidate.Text) == "" {
			return nil, ports.ErrInvalidRerankRequest
		}
		ids[index] = candidate.ID
		documents[index] = candidate.Text
	}
	payload, err := json.Marshal(map[string]any{
		"model":           h.model,
		"query":           query,
		"documents":       documents,
		"top_n":           len(documents),
		"return_document": false,
	})
	if err != nil {
		return nil, fmt.Errorf("encode rerank request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/rerank", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build rerank request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+h.apiKey)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call reranker provider: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read reranker response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrRerankerBadResponse, response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Results []struct {
			Index     int     `json:"index"`
			Relevance float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRerankerBadResponse, err)
	}
	results := make([]ports.RerankResult, 0, len(decoded.Results))
	seen := make(map[int]bool, len(decoded.Results))
	for _, item := range decoded.Results {
		if item.Index < 0 || item.Index >= len(ids) || seen[item.Index] {
			return nil, fmt.Errorf("%w: duplicate or out-of-range result index %d", ErrRerankerBadResponse, item.Index)
		}
		seen[item.Index] = true
		if math.IsNaN(item.Relevance) || math.IsInf(item.Relevance, 0) {
			return nil, fmt.Errorf("%w: non-finite relevance", ErrRerankerBadResponse)
		}
		results = append(results, ports.RerankResult{ID: ids[item.Index], Relevance: item.Relevance})
	}
	if len(results) != len(ids) {
		return nil, fmt.Errorf("%w: got %d results for %d documents", ErrRerankerBadResponse, len(results), len(ids))
	}
	return results, nil
}
