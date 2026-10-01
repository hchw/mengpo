// Package llm is the Memory LLM provider adapter. It speaks the OpenAI-compatible
// chat completions protocol over HTTP, so one implementation serves OpenAI,
// DeepSeek, Qwen, vLLM, Ollama and similar endpoints.
package llm

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

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/ports"
)

const maxResponseBytes = 1 << 20 // 1 MiB

var (
	ErrLLMDisabled              = errors.New("memory llm provider is not enabled")
	ErrLLMBadResponse           = errors.New("memory llm returned a malformed response")
	ErrExternalAnalysisDisabled = errors.New("external llm analysis is disabled")
	ErrInvalidLLMConfiguration  = errors.New("memory llm configuration is invalid")
)

// Options configure the Memory LLM adapter.
type Options struct {
	BaseURL       string
	Model         string
	APIKey        string
	Timeout       time.Duration
	MaxAttempts   int
	MaxTokens     int
	AllowExternal bool
	// Client overrides the default HTTP client (used by tests).
	Client *http.Client
}

// HTTP implements ports.MemoryAnalyst against an OpenAI-compatible endpoint.
type HTTP struct {
	baseURL       string
	model         string
	apiKey        string
	maxAttempts   int
	maxTokens     int
	allowExternal bool
	client        *http.Client
}

// NewFromConfig builds the adapter from the process configuration.
func NewFromConfig(cfg config.Config) (*HTTP, error) {
	return New(Options{
		BaseURL:       cfg.Providers.MemoryLLM.BaseURL,
		Model:         cfg.Providers.MemoryLLM.Model,
		APIKey:        cfg.Providers.MemoryLLM.APIKey,
		Timeout:       cfg.Providers.MemoryLLM.Timeout,
		MaxAttempts:   cfg.Providers.MemoryLLM.MaxAttempts,
		MaxTokens:     cfg.Analysis.MaxTokens,
		AllowExternal: cfg.Security.AllowExternalLLMAnalysis,
	})
}

func New(options Options) (*HTTP, error) {
	if !options.AllowExternal {
		// Still construct an adapter so callers can report a clear reason.
		return &HTTP{allowExternal: false}, nil
	}
	if strings.TrimSpace(options.BaseURL) == "" || strings.TrimSpace(options.Model) == "" {
		return nil, ErrInvalidLLMConfiguration
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &HTTP{
		baseURL:       strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		model:         strings.TrimSpace(options.Model),
		apiKey:        strings.TrimSpace(options.APIKey),
		maxAttempts:   maxAttempts,
		maxTokens:     options.MaxTokens,
		allowExternal: true,
		client:        client,
	}, nil
}

// Analyze renders the task prompt, calls the provider, and returns a validated
// result. It never sends a request when external analysis is disabled.
func (h *HTTP) Analyze(ctx context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.AnalystResult{}, err
	}
	if h == nil || !h.allowExternal {
		return ports.AnalystResult{}, ErrExternalAnalysisDisabled
	}
	task, err := ParseTaskType(batch.TaskType)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	system, user, err := renderPrompt(task, batch)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	started := time.Now()
	content, usage, err := h.chat(ctx, system, user)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	result, err := parseAnalystResult(content)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	usage.LatencyMS = time.Since(started).Milliseconds()
	result.Usage = &usage
	if err := analysis.ValidateResult(batch, result); err != nil {
		return ports.AnalystResult{}, fmt.Errorf("%w: %v", ErrLLMBadResponse, err)
	}
	return result, nil
}

func (h *HTTP) chat(ctx context.Context, system, user string) (string, ports.AnalysisUsage, error) {
	payload := map[string]any{
		"model": h.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0,
	}
	if h.maxTokens > 0 {
		payload["max_tokens"] = h.maxTokens
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", ports.AnalysisUsage{}, fmt.Errorf("encode chat request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= h.maxAttempts; attempt++ {
		content, usage, retryable, err := h.chatOnce(ctx, encoded)
		if err == nil {
			return content, usage, nil
		}
		lastErr = err
		if !retryable || attempt == h.maxAttempts || ctx.Err() != nil {
			break
		}
		backoff := time.Duration(attempt*attempt) * 100 * time.Millisecond
		select {
		case <-ctx.Done():
			return "", ports.AnalysisUsage{}, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return "", ports.AnalysisUsage{}, lastErr
}

func (h *HTTP) chatOnce(ctx context.Context, body []byte) (string, ports.AnalysisUsage, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", ports.AnalysisUsage{}, false, fmt.Errorf("build chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+h.apiKey)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return "", ports.AnalysisUsage{}, true, fmt.Errorf("call memory llm: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", ports.AnalysisUsage{}, true, fmt.Errorf("read memory llm response: %w", err)
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return "", ports.AnalysisUsage{}, true, fmt.Errorf("%w: status %d: %s", ErrLLMBadResponse, response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", ports.AnalysisUsage{}, false, fmt.Errorf("%w: status %d: %s", ErrLLMBadResponse, response.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", ports.AnalysisUsage{}, false, fmt.Errorf("%w: %v", ErrLLMBadResponse, err)
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return "", ports.AnalysisUsage{}, false, fmt.Errorf("%w: empty completion", ErrLLMBadResponse)
	}
	usage := ports.AnalysisUsage{PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens}
	return decoded.Choices[0].Message.Content, usage, false, nil
}

// parseAnalystResult decodes the model's JSON object into a ports.AnalystResult.
func parseAnalystResult(content string) (ports.AnalystResult, error) {
	var result ports.AnalystResult
	trimmed := strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return ports.AnalystResult{}, fmt.Errorf("%w: %v", ErrLLMBadResponse, err)
	}
	for _, classification := range result.Classifications {
		if math.IsNaN(classification.Confidence) || math.IsInf(classification.Confidence, 0) {
			return ports.AnalystResult{}, fmt.Errorf("%w: non-finite confidence", ErrLLMBadResponse)
		}
	}
	for _, candidate := range result.Candidates {
		if len(candidate.Content) > 0 && !json.Valid(candidate.Content) {
			return ports.AnalystResult{}, fmt.Errorf("%w: candidate content is not valid json", ErrLLMBadResponse)
		}
	}
	return result, nil
}

var (
	_ ports.MemoryAnalyst = (*HTTP)(nil)
)
