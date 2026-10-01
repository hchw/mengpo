package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// This file covers the session-observation path: a whole session of events is
// observed, the analyst works out which of them are worth keeping as durable
// experience, and the resulting session-scoped memory is injected back inside
// that session (and must stay hidden from other sessions).

var sessionFlags = struct {
	enabled       *bool
	mockLLM       *bool
	providerURL   *string
	providerKey   *string
	providerMdl   *string
	hostGateway   *string
	stages        *int
	requireMemory *bool
}{}

func init() {
	sessionFlags.enabled = flag.Bool("session", true, "verify the whole-session observation path")
	sessionFlags.mockLLM = flag.Bool("mock-llm", true, "provision an in-process OpenAI-compatible analyst when the tenant has no provider")
	sessionFlags.providerURL = flag.String("provider-base-url", "", "use this (real) OpenAI-compatible provider instead of the mock")
	sessionFlags.providerKey = flag.String("provider-api-key", "", "api key for -provider-base-url")
	sessionFlags.providerMdl = flag.String("provider-model", "mock-analyst", "model name for the provider")
	sessionFlags.hostGateway = flag.String("host-gateway", "", "IP the containers use to reach this host (auto-detected)")
	sessionFlags.stages = flag.Int("stages", 90, "seconds to wait for session analysis to run")
	sessionFlags.requireMemory = flag.Bool("session-memory", false, "also verify that the session produced and injected a retained memory (needs a real LLM)")
}

// sessionEvent is one observation pushed into a session.
type sessionEvent struct {
	MessageType string
	Text        string
	Payload     map[string]any
}

// transcript is a realistic slice of a working session: failures, corrections and
// explicit remember/forget instructions.
var transcript = []sessionEvent{
	{"tool_result", "migration 000006 在重试时失败,报 duplicate column", map[string]any{"status": "error"}},
	{"user_correction", "不对,那个报错是因为我重复执行了同一个迁移", map[string]any{"corrects": "previous"}},
	{"user_remember", "记住:这个项目的迁移脚本必须幂等,重跑不能报错", map[string]any{"remember": true}},
	{"user_remember", "记住:本地跑集成测试要用 55432 端口的 postgres", map[string]any{"remember": true}},
	{"tool_result", "go test ./internal/... 全部通过", map[string]any{"status": "ok"}},
}

// plainEvents carry no memory intent at all; they must never reach the model.
var plainEvents = []sessionEvent{
	{"tool_result", "ls -la 列出 12 个文件", map[string]any{"status": "ok"}},
	{"tool_result", "git status 工作区干净", map[string]any{"status": "ok"}},
}

func runSessionFlow(ctx context.Context, c *client) error {
	step("6. 整段会话观测 -> 触发分析(规则预筛)")

	sessionA, err := c.createSession(ctx)
	if err != nil {
		return err
	}
	sessionB, err := c.createSession(ctx)
	if err != nil {
		return err
	}
	ok("会话 A=%s", sessionA)
	ok("会话 B=%s(对照,只放普通事件)", sessionB)

	beforeRuns, err := c.analysisRunCount(ctx)
	if err != nil {
		return err
	}
	for _, event := range transcript {
		if err := c.observe(ctx, sessionA, event); err != nil {
			return fmt.Errorf("写入会话观测失败: %w", err)
		}
	}
	ok("写入 %d 条会话观测(失败 / 纠正 / 两条记住)", len(transcript))
	for _, event := range plainEvents {
		if err := c.observe(ctx, sessionB, event); err != nil {
			return fmt.Errorf("写入对照观测失败: %w", err)
		}
	}
	ok("写入 %d 条无记忆意图的对照观测", len(plainEvents))

	runs, err := c.waitForRuns(ctx, beforeRuns, *sessionFlags.stages)
	if err != nil {
		return err
	}
	if runs > beforeRuns {
		ok("整段会话触发了 %d 条分析运行(规则预筛命中)", runs-beforeRuns)
	} else {
		fail("整段会话没有触发任何分析运行(规则预筛可能失效)")
	}

	if !*sessionFlags.requireMemory {
		info("已跳过「分析产出记忆并注入」的校验(")
		info("  该环节需要真实 LLM 才能产出候选;加 -session-memory 开启完整校验)")
		return nil
	}

	step("6.1 分析产出有效经验(需要真实 LLM)")
	provider, err := provisionProvider(ctx, c)
	if err != nil {
		return err
	}
	if provider == "" {
		info("跳过:租户没有可用的 LLM 提供方")
		return nil
	}
	ok("分析提供方就绪:%s", provider)

	memories, _, err := c.waitForSessionMemories(ctx, sessionA, *sessionFlags.stages)
	if err != nil {
		return err
	}
	if len(memories) == 0 {
		fail("整段会话分析没有产出任何会话级记忆")
		return nil
	}
	ok("产出 %d 条会话级记忆", len(memories))
	for index, memory := range memories {
		if index == 3 {
			break
		}
		info("· [%s] %s", memory.Status, truncate(memory.ContentText, 44))
	}
	for _, memory := range memories {
		if memory.Status == "candidate" {
			if err := c.confirmCandidate(ctx, memory.ID); err != nil {
				fail("确认候选失败: %v", err)
			} else {
				ok("候选 %s 已确认为正式会话记忆", memory.ID)
			}
			break
		}
	}

	step("6.2 会话内注入与会话隔离")
	inSession, err := c.projectInScope(ctx, sessionA, "pgvector", *tokenBudget)
	if err != nil {
		return err
	}
	injectedIDs := map[string]bool{}
	for _, id := range inSession.injected() {
		injectedIDs[id] = true
	}
	hit := false
	for _, memory := range memories {
		if injectedIDs[memory.ID] {
			hit = true
		}
	}
	if hit {
		ok("会话 A 内注入到会话记忆(共 %d 条)", len(inSession.injected()))
	} else {
		fail("会话 A 内没有注入会话记忆:注入 %d 条", len(inSession.injected()))
	}
	other, err := c.projectInScope(ctx, sessionB, "pgvector", *tokenBudget)
	if err != nil {
		return err
	}
	leaked := 0
	for _, id := range other.injected() {
		if injectedIDs[id] {
			leaked++
		}
	}
	if leaked == 0 {
		ok("会话 B 看不到会话 A 的记忆(隔离正确)")
	} else {
		fail("会话 A 的 %d 条记忆泄漏到会话 B", leaked)
	}
	return nil
}

// waitForRuns waits until the analysis run count grows past the baseline.
func (c *client) waitForRuns(ctx context.Context, baseline, seconds int) (int, error) {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		runs, err := c.analysisRunCount(ctx)
		if err != nil {
			return 0, err
		}
		if runs > baseline || time.Now().After(deadline) {
			return runs, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ---- provider provisioning ----

// provisionProvider ensures the tenant has a usable analyst. It returns a
// human-readable description, or "" when none is available.
func provisionProvider(ctx context.Context, c *client) (string, error) {
	if err := c.useScope("user-global", ""); err != nil {
		return "", err
	}
	if *sessionFlags.providerURL != "" {
		if err := c.putProvider(ctx, *sessionFlags.providerURL, *sessionFlags.providerMdl, *sessionFlags.providerKey); err != nil {
			return "", err
		}
		return *sessionFlags.providerURL, nil
	}
	view, err := c.providerView(ctx)
	if err != nil {
		return "", err
	}
	if view.Enabled && view.BaseURL != "" {
		return view.BaseURL, nil
	}
	if !*sessionFlags.mockLLM {
		return "", nil
	}
	server, url, err := startMockLLM()
	if err != nil {
		return "", err
	}
	go func() { <-ctx.Done(); _ = server.Close() }()
	if err := c.putProvider(ctx, url, *sessionFlags.providerMdl, "mock-key"); err != nil {
		return "", err
	}
	probe, err := c.probeProvider(ctx, url, *sessionFlags.providerMdl, "mock-key")
	if err != nil {
		return "", err
	}
	if !probe.OK {
		return "", fmt.Errorf("内置分析模型连通性测试失败: %s", probe.Error)
	}
	return url + "(内置 mock)", nil
}

// startMockLLM runs an OpenAI-compatible analyst inside the tool process. The
// service containers reach it through the host gateway address.
func startMockLLM() (*http.Server, string, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &request)
		content := "{}"
		for _, message := range request.Messages {
			if message.Role == "user" {
				content = mockAnalystReply(message.Content)
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"model":   request.Model,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 64, "completion_tokens": 32, "total_tokens": 96},
		})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	host := *sessionFlags.hostGateway
	if host == "" {
		host = detectHostGateway()
	}
	return server, fmt.Sprintf("http://%s:%d/v1", host, port), nil
}

// detectHostGateway returns the address containers use to reach this host.
func detectHostGateway() string {
	out, err := exec.Command("docker", "network", "inspect", "mengpo-dev_default",
		"-f", "{{(index .IPAM.Config 0).Gateway}}").Output()
	if err == nil {
		if gateway := strings.TrimSpace(string(out)); net.ParseIP(gateway) != nil {
			return gateway
		}
	}
	return "172.20.0.1"
}

// mockAnalystReply returns a well-formed, evidence-grounded answer for the task
// the application asked about, parsed straight out of the prompt.
func mockAnalystReply(prompt string) string {
	inputs := map[string]any{}
	if marker := strings.Index(prompt, "\nInput:\n"); marker >= 0 {
		_ = json.Unmarshal([]byte(prompt[marker+len("\nInput:\n"):]), &inputs)
	}
	events, _ := inputs["events"].([]any)
	eventID, sessionID, text := "", "", ""
	if len(events) > 0 {
		if first, ok := events[0].(map[string]any); ok {
			eventID, _ = first["event_id"].(string)
			sessionID, _ = first["session_id"].(string)
			if payload, ok := first["payload"].(map[string]any); ok {
				text, _ = payload["text"].(string)
			}
		}
	}
	if eventID == "" {
		return "{}"
	}
	task, _ := inputs["task_type"].(string)
	switch task {
	case "analyze_failure":
		return jsonString(map[string]any{"failures": []map[string]any{{
			"event_ids":   []string{eventID},
			"conclusion":  firstNonEmpty(text, "会话中出现一次工具失败"),
			"attribution": "inferred",
			"confidence":  0.8,
		}}})
	case "classify_event":
		return jsonString(map[string]any{"classifications": []map[string]any{{"event_id": eventID, "category": "session_event", "confidence": 0.7}}})
	case "analyze_conflict":
		return jsonString(map[string]any{"conflicts": []any{}})
	case "propose_transfer":
		return jsonString(map[string]any{"candidates": []any{}})
	default:
		scopeType, scopeID := "user-global", ""
		if sessionID != "" {
			scopeType, scopeID = "session", sessionID
		}
		if scopeID == "" {
			return "{}"
		}
		return jsonString(map[string]any{"candidates": []map[string]any{{
			"candidate_id":       "session-experience-1",
			"evidence_event_ids": []string{eventID},
			"scope_type":         scopeType,
			"scope_id":           scopeID,
			"content":            map[string]any{"text": firstNonEmpty(text, "会话中值得记住的经验")},
			"confidence":         0.78,
		}}})
	}
}

func jsonString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ---- session-aware client helpers ----

func (c *client) useScope(scopeType, sessionID string) error {
	switch scopeType {
	case "user-global", "":
		c.scopeType = "user-global"
		c.scopeSession = ""
	case "session":
		if sessionID == "" {
			return fmt.Errorf("session scope requires a session id")
		}
		c.scopeType = "session"
		c.scopeSession = sessionID
	default:
		return fmt.Errorf("unsupported scope %q", scopeType)
	}
	return nil
}

func (c *client) createSession(ctx context.Context) (string, error) {
	var response struct {
		Data struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/api/v1/sessions", c.envelope(map[string]any{}, nil), &response); err != nil {
		return "", err
	}
	return response.Data.SessionID, nil
}

func (c *client) observe(ctx context.Context, sessionID string, event sessionEvent) error {
	payload := map[string]any{
		"message_type": event.MessageType,
		"text":         event.Text,
		"payload":      event.Payload,
		"occurred_at":  time.Now().UTC(),
	}
	return c.postInScope(ctx, "/api/v1/observe", sessionID, c.envelope(payload, nil), nil)
}

type sessionMemory struct {
	ID          string
	Status      string
	ContentText string
	ScopeType   string
}

// waitForSessionMemories polls the session until the analyst produced memories.
func (c *client) waitForSessionMemories(ctx context.Context, sessionID string, seconds int) ([]sessionMemory, int, error) {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	var memories []sessionMemory
	for {
		current, err := c.listSessionMemories(ctx, sessionID)
		if err != nil {
			return nil, 0, err
		}
		if len(current) > 0 {
			memories = current
			break
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	runs, err := c.analysisRunCount(ctx)
	if err != nil {
		return nil, 0, err
	}
	return memories, runs, nil
}

func (c *client) listSessionMemories(ctx context.Context, sessionID string) ([]sessionMemory, error) {
	var response struct {
		Data struct {
			Items []struct {
				ID          string `json:"id"`
				Status      string `json:"status"`
				ScopeType   string `json:"scope_type"`
				Summary     string `json:"content_summary"`
				ContentText string `json:"content_text"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := c.postInScope(ctx, "/api/v1/candidates", sessionID, c.envelope(map[string]any{"page": 1, "page_size": 50}, nil), &response); err != nil {
		return nil, err
	}
	memories := make([]sessionMemory, 0, len(response.Data.Items))
	for _, item := range response.Data.Items {
		text := item.ContentText
		if text == "" {
			text = item.Summary
		}
		memories = append(memories, sessionMemory{ID: item.ID, Status: item.Status, ContentText: text, ScopeType: item.ScopeType})
	}
	return memories, nil
}

func (c *client) confirmCandidate(ctx context.Context, id string) error {
	return c.post(ctx, "/api/v1/candidates/"+id+"/confirm", c.envelope(map[string]any{}, nil), nil)
}

func (c *client) analysisRunCount(ctx context.Context) (int, error) {
	var response struct {
		Data struct {
			Items []json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/api/v1/analysis-runs", c.envelope(map[string]any{"page": 1, "page_size": 200}, nil), &response); err != nil {
		return 0, err
	}
	return len(response.Data.Items), nil
}

func (c *client) projectInScope(ctx context.Context, sessionID, query string, tokens int) (projectionResponse, error) {
	if err := c.useScope("session", sessionID); err != nil {
		return projectionResponse{}, err
	}
	defer func() { _ = c.useScope("user-global", "") }()
	body := c.envelope(map[string]any{"query": query, "memory_type": "session"}, nil)
	body["budget"] = map[string]any{"injection_tokens": tokens}
	var response projectionResponse
	err := c.post(ctx, "/api/v1/project", body, &response)
	return response, err
}

func (c *client) putProvider(ctx context.Context, baseURL, model, apiKey string) error {
	if err := c.useScope("user-global", ""); err != nil {
		return err
	}
	payload := map[string]any{"enabled": true, "base_url": baseURL, "model": model}
	if apiKey != "" {
		payload["api_key"] = apiKey
	}
	// PUT is the write verb; POST /api/v1/providers only reads the view.
	return c.put(ctx, "/api/v1/providers", c.envelope(payload, nil), nil)
}

type providerView struct {
	Enabled bool
	BaseURL string
	Source  string
}

func (c *client) providerView(ctx context.Context) (providerView, error) {
	var current struct {
		Data struct {
			Enabled bool   `json:"enabled"`
			BaseURL string `json:"base_url"`
			Source  string `json:"source"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/api/v1/providers", c.envelope(map[string]any{}, nil), &current); err != nil {
		return providerView{}, err
	}
	return providerView{Enabled: current.Data.Enabled, BaseURL: current.Data.BaseURL, Source: current.Data.Source}, nil
}

type probeResult struct {
	OK    bool
	Error string
}

func (c *client) probeProvider(ctx context.Context, baseURL, model, apiKey string) (probeResult, error) {
	var result struct {
		Data struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"data"`
	}
	payload := map[string]any{"enabled": true, "base_url": baseURL, "model": model}
	if apiKey != "" {
		payload["api_key"] = apiKey
	}
	if err := c.post(ctx, "/api/v1/providers/test", c.envelope(payload, nil), &result); err != nil {
		return probeResult{}, err
	}
	return probeResult{OK: result.Data.OK, Error: result.Data.Error}, nil
}
