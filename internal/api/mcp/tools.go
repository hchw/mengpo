package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hchw/mengpo/internal/api/dto"
)

// ---- tool input schemas ----

type recallInput struct {
	Query          string   `json:"query" jsonschema:"The question or topic to recall memories for"`
	SessionID      string   `json:"session_id,omitempty" jsonschema:"Session scope identifier from memory_bind_session; set it to recall session-scoped memories"`
	UserID         string   `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	Scope          string   `json:"scope,omitempty" jsonschema:"user-global or session; defaults from session_id"`
	Mode           string   `json:"mode,omitempty" jsonschema:"auto, focus, or diverge"`
	Topics         []string `json:"topics,omitempty" jsonschema:"Focus topics for the memory hint"`
	IdempotencyKey string   `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

type rememberInput struct {
	Text           string `json:"text" jsonschema:"One durable fact, preference, convention, or decision, stated concisely and unambiguously; capture the user's confirmation or new decision"`
	SessionID      string `json:"session_id,omitempty" jsonschema:"Session scope identifier from memory_bind_session; omit to store user-global"`
	UserID         string `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

type forgetInput struct {
	Text           string `json:"text" jsonschema:"The stored fact, preference, or convention the user rejected, reversed, or wants retired"`
	UserID         string `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

type observeInput struct {
	SourceType     string         `json:"source_type,omitempty" jsonschema:"user, agent, tool, workflow, or gateway; defaults to user"`
	MessageType    string         `json:"message_type,omitempty" jsonschema:"Event type such as user_message, user_feedback (dissatisfaction/approval), or tool.result"`
	Text           string         `json:"text,omitempty" jsonschema:"Raw event text: what the user said or how they reacted, verbatim when possible"`
	Payload        map[string]any `json:"payload,omitempty" jsonschema:"Structured event payload, e.g. {\"sentiment\":\"negative\",\"topic\":\"...\"}"`
	SessionID      string         `json:"session_id,omitempty" jsonschema:"Session scope identifier from memory_bind_session"`
	UserID         string         `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	IdempotencyKey string         `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

type correctInput struct {
	MemoryID       string `json:"memory_id" jsonschema:"Identifier of the memory being corrected, from memory_recall or an injected memory"`
	Text           string `json:"text,omitempty" jsonschema:"The corrected version of the memory to replace the wrong one"`
	Reason         string `json:"reason,omitempty" jsonschema:"Why the memory is wrong, e.g. the user's exact objection"`
	Type           string `json:"type,omitempty" jsonschema:"Feedback type; defaults to corrected"`
	SessionID      string `json:"session_id,omitempty" jsonschema:"Session scope identifier from memory_bind_session"`
	UserID         string `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

type bindSessionInput struct {
	ExternalID     string `json:"external_id,omitempty" jsonschema:"Caller-side session identifier"`
	Title          string `json:"title,omitempty" jsonschema:"Human-readable session title"`
	UserID         string `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

type consolidateInput struct {
	SessionID      string `json:"session_id,omitempty" jsonschema:"Session scope identifier from memory_bind_session; omit to consolidate user-global memory"`
	UserID         string `json:"user_id,omitempty" jsonschema:"Bound user when the agent serves several users"`
	Scope          string `json:"scope,omitempty" jsonschema:"user-global or session; defaults from session_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Optional retry-safe idempotency key"`
}

// ---- tool definitions ----

// boolPtr returns a pointer to value, for the optional annotation hints whose
// zero value (false) differs from "unset".
func boolPtr(value bool) *bool { return &value }

func recallTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_recall",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:         "Recall memory",
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(false),
		},
		Description: `Recall long-term memories (facts, preferences, project conventions, past decisions, and anything the user previously asked you to remember) that are relevant to a query.

Call this BEFORE answering whenever: the user references earlier work or prior decisions ("last time", "previously", "as we discussed", "do you remember"); the user starts or switches to a new task or topic; the user asks about their own preferences, conventions, or history; or project rules and prior mistakes might apply. When in doubt, recall first — an empty result is cheap, and answered-from-memory is always better than guessed.

Returns memory text plus the memory_id and projection_id needed to reference, correct, or forget those memories.`,
	}
}

func rememberTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_remember",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Remember",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
		},
		Description: `Persist a durable fact, preference, project convention, or decision to long-term memory so later sessions can recall it. Reliable recording is the point: if the user told you something durable, record it now — do not rely on the transcript surviving.

Call this in the SAME turn the user states or CONFIRMS something durable. Typical triggers:
- The user confirms a choice or approves what you proposed ("yes", "that's right", "go with that", "use X").
- The user states a preference, rule, constraint, or convention ("we always...", "never...", "prefer...", "from now on...").
- The user asks you to remember something, or repeats something you have clearly forgotten.
- The user REJECTS or overrides an earlier decision: first invalidate the superseded memory with memory_forget, then remember the new decision here.

Record one focused fact per call. Skip one-off, transient, or easily re-derived details (exact commands already in the repo, values you can read from files).

Scope: pass the session_id returned by memory_bind_session to attach the memory to the current session; omit it to store a durable user-global memory. Omitting it never drops the memory.`,
	}
}

func forgetTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_forget",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Forget memory",
			DestructiveHint: boolPtr(true),
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
		},
		Description: `Invalidate/retire a stored fact, preference, or convention so it stops being recalled.

Call this when the user rejects, reverses, or disputes a previously stored memory: "no, don't do that anymore", "forget that", "we changed our mind", "that's outdated", or "that's not how it works".

This retires the OLD memory. If the user also states a replacement, call memory_forget for the old memory and then memory_remember the new one in the same turn — never leave the old and the new both active. Use memory_correct instead when you have the exact memory_id and want to fix its content.`,
	}
}

func observeTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_observe",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Observe event",
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		},
		Description: `Report a raw session event (a user message, tool result, or workflow step) as an observation.

Call this to capture user BEHAVIOR and SENTIMENT that should shape future interactions but is not a clean durable statement — for example the user is dissatisfied or annoyed with a result, frustrated by a repeated mistake, reacts strongly to an approach, or gives negative feedback without stating a new rule. One event per call; keep the original wording in text and any structured signals in payload.

Advanced: prefer memory_remember for explicit durable instructions, and memory_forget/memory_correct to retire or fix memories. Do not fabricate events or invent details the user did not express.

Scope: pass the session_id returned by memory_bind_session to allow session working memory; omit it to record a durable user-global observation. Omitting it never drops the observation.`,
	}
}

func correctTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_correct",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Correct memory",
			DestructiveHint: boolPtr(true),
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
		},
		Description: `Report that a specific recalled memory is wrong, and optionally supply the corrected version.

Call this when the user disputes or corrects a memory you recalled or injected ("that's not right", "it's actually Y", "that memory is outdated"). Requires a real memory_id taken from memory_recall or an injected memory; never invent an id.

Use memory_forget instead when the memory should simply be dropped, and memory_remember for a brand-new fact that was not stored before.`,
	}
}

func bindSessionTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_bind_session",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Bind session",
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		},
		Description: `Bind this conversation to a memory session and return its session_id.

Call this once at the start of a conversation when you intend to use session-scoped memory. Pass the returned session_id to memory_recall, memory_remember, or memory_observe so facts are recorded and fetched at session scope; session memory is the working context that can later be consolidated into durable user-global memory. Without a session_id those calls default to user-global scope.`,
	}
}

func consolidateTool() *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name: "memory_consolidate",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Consolidate memory",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
		},
		Description: `Trigger asynchronous consolidation of session or user-global memories into durable, higher-confidence memory. Returns a run handle; it does not block on completion.

Call this when a session is wrapping up, when the user asks to save/summarize what was learned, or when session-scoped facts should become recallable across future sessions. Run it after the important facts for the session have been recorded with memory_remember; consolidation promotes and merges them, it does not create facts from nothing.`,
	}
}

// ---- tool handlers ----

func (h *Handler) recall(identity Identity) sdkmcp.ToolHandlerFor[recallInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in recallInput) (*sdkmcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Query) == "" {
			return invalidInput("query is required"), nil, nil
		}
		var hint *dto.MemoryHint
		if in.Mode != "" || len(in.Topics) > 0 {
			hint = &dto.MemoryHint{Mode: in.Mode, Topics: in.Topics}
		}
		envelope, err := h.envelope(identity, in.UserID, in.SessionID, in.Scope, idempotency(in.IdempotencyKey, "recall", in.Query, in.SessionID), map[string]any{"query": in.Query}, hint)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Project(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		return renderProjection(result), nil, nil
	}
}

func (h *Handler) remember(identity Identity) sdkmcp.ToolHandlerFor[rememberInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in rememberInput) (*sdkmcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Text) == "" {
			return invalidInput("text is required"), nil, nil
		}
		payload := map[string]any{
			"source_type":  "user",
			"message_type": "user_remember",
			"text":         in.Text,
			// The nested payload is what the server's deterministic capture and
			// analysis read: the event text and the remember flag must live here,
			// not only in the top-level text field (which maps to payload_text).
			"payload": map[string]any{"remember": true, "text": in.Text},
		}
		envelope, err := h.envelope(identity, in.UserID, in.SessionID, "", idempotency(in.IdempotencyKey, "remember", in.Text), payload, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Observe(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		return renderAccepted("remembered", result), nil, nil
	}
}

func (h *Handler) forget(identity Identity) sdkmcp.ToolHandlerFor[forgetInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in forgetInput) (*sdkmcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Text) == "" {
			return invalidInput("text is required"), nil, nil
		}
		payload := map[string]any{
			"source_type":  "user",
			"message_type": "user_forget",
			"text":         in.Text,
			"payload":      map[string]any{"forget": true, "text": in.Text},
		}
		envelope, err := h.envelope(identity, in.UserID, "", "user-global", idempotency(in.IdempotencyKey, "forget", in.Text), payload, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Observe(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		return renderAccepted("forgotten", result), nil, nil
	}
}

func (h *Handler) observe(identity Identity) sdkmcp.ToolHandlerFor[observeInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in observeInput) (*sdkmcp.CallToolResult, any, error) {
		sourceType := in.SourceType
		if sourceType == "" {
			sourceType = "user"
		}
		payload := map[string]any{"source_type": sourceType, "message_type": in.MessageType}
		// Event data travels nested under "payload"; the top-level text is only
		// the raw payload_text. Keep both in sync so capture/analysis see the text.
		data := map[string]any{}
		if in.Text != "" {
			payload["text"] = in.Text
			data["text"] = in.Text
		}
		for key, value := range in.Payload {
			data[key] = value
		}
		if len(data) > 0 {
			payload["payload"] = data
		}
		envelope, err := h.envelope(identity, in.UserID, in.SessionID, "", idempotency(in.IdempotencyKey, "observe", sourceType, in.MessageType, in.Text, in.SessionID), payload, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Observe(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		return renderAccepted("observed", result), nil, nil
	}
}

func (h *Handler) correct(identity Identity) sdkmcp.ToolHandlerFor[correctInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in correctInput) (*sdkmcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.MemoryID) == "" {
			return invalidInput("memory_id is required; never invent one"), nil, nil
		}
		feedbackType := in.Type
		if feedbackType == "" {
			feedbackType = "corrected"
		}
		reason := in.Reason
		if reason == "" {
			reason = in.Text
		}
		payload := map[string]any{"memory_id": in.MemoryID, "type": feedbackType, "reason": reason}
		envelope, err := h.envelope(identity, in.UserID, in.SessionID, "", idempotency(in.IdempotencyKey, "correct", in.MemoryID, feedbackType, reason), payload, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Feedback(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		return renderAccepted("correction reported", result), nil, nil
	}
}

func (h *Handler) bindSession(identity Identity) sdkmcp.ToolHandlerFor[bindSessionInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in bindSessionInput) (*sdkmcp.CallToolResult, any, error) {
		payload := map[string]any{"external_id": in.ExternalID, "title": in.Title}
		envelope, err := h.envelope(identity, in.UserID, "", "user-global", idempotency(in.IdempotencyKey, "bind_session", in.ExternalID, in.UserID, in.Title), payload, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Session(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		fields := resultMap(result)
		sessionID, _ := fields["session_id"].(string)
		return &sdkmcp.CallToolResult{
			Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: "session_id: " + sessionID}},
			StructuredContent: map[string]any{"session_id": sessionID},
		}, nil, nil
	}
}

func (h *Handler) consolidate(identity Identity) sdkmcp.ToolHandlerFor[consolidateInput, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in consolidateInput) (*sdkmcp.CallToolResult, any, error) {
		envelope, err := h.envelope(identity, in.UserID, in.SessionID, in.Scope, idempotency(in.IdempotencyKey, "consolidate", in.Scope, in.SessionID, in.UserID), map[string]any{}, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		result, err := h.UseCases.Consolidate(WithIdentity(ctx, identity), envelope)
		if err != nil {
			return toolError(err), nil, nil
		}
		return renderAccepted("consolidation accepted", result), nil, nil
	}
}

// ---- envelope assembly ----

func (h *Handler) envelope(identity Identity, userID, sessionID, scope, key string, payload map[string]any, hint *dto.MemoryHint) (dto.Envelope, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = identity.UserID
	}
	scopeType := strings.TrimSpace(scope)
	if scopeType == "" {
		if strings.TrimSpace(sessionID) != "" {
			scopeType = "session"
		} else {
			scopeType = "user-global"
		}
	}
	if scopeType != "session" && scopeType != "user-global" {
		return dto.Envelope{}, fmt.Errorf("scope must be user-global or session")
	}
	if scopeType == "session" && strings.TrimSpace(sessionID) == "" {
		return dto.Envelope{}, fmt.Errorf("session scope requires session_id")
	}
	principalType, principalID := "agent", identity.AgentID
	if principalID == "" {
		principalType, principalID = "user", userID
	}
	if principalID == "" {
		principalID = identity.UserID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return dto.Envelope{}, err
	}
	return dto.Envelope{
		Version:        dto.CurrentVersion,
		RequestID:      newRequestID(),
		IdempotencyKey: key,
		Principal:      dto.Principal{Type: principalType, ID: principalID},
		Scope:          dto.Scope{TenantID: identity.TenantID, UserID: userID, Type: scopeType, SessionID: sessionID},
		MemoryHint:     hint,
		Privacy:        dto.Privacy{Visibility: "private"},
		Payload:        raw,
	}, nil
}

func idempotency(explicit string, parts ...string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return deriveKey(parts...)
}

func deriveKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "mcp:" + hex.EncodeToString(sum[:])[:24]
}

// ---- results ----

type codedError interface {
	error
	APIErrorCode() string
	Retryable() bool
}

func toolError(err error) *sdkmcp.CallToolResult {
	code := "INTERNAL_ERROR"
	message := "the operation failed"
	var coded codedError
	if errors.As(err, &coded) {
		code = coded.APIErrorCode()
		if text := strings.TrimSpace(err.Error()); text != "" {
			message = text
		}
	}
	return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: code + ": " + message}}}
}

func invalidInput(message string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "INVALID_INPUT: " + message}}}
}

func renderAccepted(verb string, result any) *sdkmcp.CallToolResult {
	result = resultOrEmpty(result)
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: verb}},
		StructuredContent: resultMap(result),
	}
}

func resultOrEmpty(result any) any {
	if result == nil {
		return map[string]any{}
	}
	return result
}

func resultMap(result any) map[string]any {
	raw, err := json.Marshal(resultOrEmpty(result))
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

type projectionBody struct {
	ProjectionID string `json:"projection_id"`
	Items        []struct {
		Text      string `json:"Text"`
		Candidate struct {
			Node struct {
				ID string `json:"ID"`
			} `json:"Node"`
		} `json:"Candidate"`
	} `json:"items"`
	Metadata struct {
		Mode     string `json:"mode"`
		Degraded bool   `json:"degraded"`
	} `json:"metadata"`
}

func renderProjection(result any) *sdkmcp.CallToolResult {
	raw, err := json.Marshal(result)
	if err != nil {
		return toolError(err)
	}
	var body projectionBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return toolError(err)
	}
	lines := make([]string, 0, len(body.Items))
	items := make([]map[string]string, 0, len(body.Items))
	for _, item := range body.Items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		lines = append(lines, "- "+text)
		items = append(items, map[string]string{"memory_id": item.Candidate.Node.ID, "text": text})
	}
	text := "No memories matched."
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
		StructuredContent: map[string]any{
			"projection_id": body.ProjectionID,
			"mode":          body.Metadata.Mode,
			"degraded":      body.Metadata.Degraded,
			"items":         items,
		},
	}
}
