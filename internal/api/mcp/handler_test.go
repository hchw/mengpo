package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hchw/mengpo/internal/api/dto"
)

type fakeUseCases struct {
	observed       []dto.Envelope
	projected      []dto.Envelope
	feedback       []dto.Envelope
	sessions       []dto.Envelope
	consolidated   []dto.Envelope
	projectResult  any
	projectErr     error
	observeResult  any
	observeErr     error
	feedbackErr    error
	sessionErr     error
	consolidateErr error
}

func (f *fakeUseCases) Observe(_ context.Context, e dto.Envelope) (any, error) {
	f.observed = append(f.observed, e)
	return f.observeResult, f.observeErr
}
func (f *fakeUseCases) Project(_ context.Context, e dto.Envelope) (any, error) {
	f.projected = append(f.projected, e)
	return f.projectResult, f.projectErr
}
func (f *fakeUseCases) Consolidate(_ context.Context, e dto.Envelope) (any, error) {
	f.consolidated = append(f.consolidated, e)
	return map[string]any{"run_id": "run-1", "accepted": true}, f.consolidateErr
}
func (f *fakeUseCases) Feedback(_ context.Context, e dto.Envelope) (any, error) {
	f.feedback = append(f.feedback, e)
	return map[string]any{"stored": true}, f.feedbackErr
}
func (f *fakeUseCases) Session(_ context.Context, e dto.Envelope) (any, error) {
	f.sessions = append(f.sessions, e)
	return map[string]any{"session_id": "session-a", "tenant_id": "tenant-a"}, f.sessionErr
}

const testSecret = "agent-secret"

func testIdentity(capabilities ...string) Identity {
	return Identity{TenantID: "tenant-a", UserID: "user-a", AgentID: "agent-a", Capabilities: capabilities}
}

func testHandler(t *testing.T, useCases UseCases, identity Identity) *httptest.Server {
	t.Helper()
	handler := &Handler{
		UseCases: useCases,
		Resolve: func(_ context.Context, credential string) (Identity, error) {
			if credential != testSecret {
				return Identity{}, errors.New("bad credential")
			}
			return identity, nil
		},
	}
	server := httptest.NewServer(handler.HTTPHandler())
	t.Cleanup(server.Close)
	return server
}

type authTransport struct {
	secret string
	base   http.RoundTripper
}

func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.secret)
	return t.base.RoundTrip(cloned)
}

func connect(t *testing.T, endpoint, secret string) *sdkmcp.ClientSession {
	t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: authTransport{secret: secret, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolNames(t *testing.T, session *sdkmcp.ClientSession) map[string]bool {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestHTTPHandlerRejectsMissingCredential(t *testing.T) {
	server := testHandler(t, &fakeUseCases{}, testIdentity("project"))
	response, err := http.Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", response.StatusCode)
	}
	if got := response.Header.Get("WWW-Authenticate"); got == "" {
		t.Fatal("missing WWW-Authenticate header")
	}
}

func TestToolListFilteredByCapability(t *testing.T) {
	projectOnly := connect(t, testHandler(t, &fakeUseCases{}, testIdentity("project")).URL, testSecret)
	names := toolNames(t, projectOnly)
	if !names["memory_recall"] || len(names) != 1 {
		t.Fatalf("project-only tools=%v want only memory_recall", names)
	}

	full := connect(t, testHandler(t, &fakeUseCases{}, testIdentity("observe", "project", "feedback", "session", "consolidate")).URL, testSecret)
	all := toolNames(t, full)
	for _, want := range []string{"memory_recall", "memory_remember", "memory_forget", "memory_observe", "memory_correct", "memory_bind_session", "memory_consolidate"} {
		if !all[want] {
			t.Fatalf("missing tool %q in %v", want, all)
		}
	}
}

func TestRecallRendersTextAndIdentifiers(t *testing.T) {
	projection := map[string]any{
		"projection_id": "proj-1",
		"items": []any{
			map[string]any{"Text": "uses pgvector", "Candidate": map[string]any{"Node": map[string]any{"ID": "mem-1"}}},
			map[string]any{"Text": "tenant-per-schema", "Candidate": map[string]any{"Node": map[string]any{"ID": "mem-2"}}},
		},
		"metadata": map[string]any{"mode": "focus", "degraded": false},
	}
	useCases := &fakeUseCases{projectResult: projection}
	session := connect(t, testHandler(t, useCases, testIdentity("project")).URL, testSecret)

	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_recall", Arguments: map[string]any{"query": "storage"}})
	if err != nil {
		t.Fatalf("call recall: %v", err)
	}
	if result.IsError {
		t.Fatalf("recall errored: %v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content=%#v", result.StructuredContent)
	}
	if structured["projection_id"] != "proj-1" {
		t.Fatalf("projection id=%v", structured["projection_id"])
	}
	items, _ := structured["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	if len(useCases.projected) != 1 || useCases.projected[0].Scope.Type != "user-global" {
		t.Fatalf("envelope=%#v", useCases.projected)
	}
	if err := useCases.projected[0].Validate(); err != nil {
		t.Fatalf("envelope invalid: %v", err)
	}
}

func TestRememberDerivesStableIdempotency(t *testing.T) {
	useCases := &fakeUseCases{observeResult: map[string]any{"event_id": "event-1", "created": true}}
	session := connect(t, testHandler(t, useCases, testIdentity("observe")).URL, testSecret)
	for i := 0; i < 2; i++ {
		if _, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_remember", Arguments: map[string]any{"text": "prefers tabs"}}); err != nil {
			t.Fatalf("call remember: %v", err)
		}
	}
	if len(useCases.observed) != 2 {
		t.Fatalf("observed=%d", len(useCases.observed))
	}
	first := useCases.observed[0].IdempotencyKey
	if first == "" || first != useCases.observed[1].IdempotencyKey {
		t.Fatalf("idempotency keys not content-derived: %q vs %q", first, useCases.observed[1].IdempotencyKey)
	}
	// An explicit key wins.
	if _, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_remember", Arguments: map[string]any{"text": "prefers tabs", "idempotency_key": "explicit-1"}}); err != nil {
		t.Fatalf("call remember explicit: %v", err)
	}
	if got := useCases.observed[2].IdempotencyKey; got != "explicit-1" {
		t.Fatalf("explicit key=%q", got)
	}
}

func TestCorrectRequiresRealMemoryID(t *testing.T) {
	useCases := &fakeUseCases{}
	session := connect(t, testHandler(t, useCases, testIdentity("feedback")).URL, testSecret)
	// memory_id is a required field, so the schema rejects the call before the
	// handler runs; either way the feedback must not be recorded.
	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_correct", Arguments: map[string]any{"text": "no id"}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("missing memory_id must be rejected: result=%#v err=%v", result, err)
	}
	if len(useCases.feedback) != 0 {
		t.Fatal("feedback must not be recorded without a memory_id")
	}
}

type deniedError struct{}

func (deniedError) Error() string        { return `capability "session" is not granted` }
func (deniedError) APIErrorCode() string { return "CAPABILITY_DENIED" }
func (deniedError) Retryable() bool      { return false }

func TestCapabilityDenialIsReportedAsStableError(t *testing.T) {
	useCases := &fakeUseCases{sessionErr: deniedError{}}
	session := connect(t, testHandler(t, useCases, testIdentity("session")).URL, testSecret)
	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_bind_session", Arguments: map[string]any{"external_id": "ext-1"}})
	if err != nil {
		t.Fatalf("call bind_session: %v", err)
	}
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("result=%#v", result)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || !contains(text.Text, "CAPABILITY_DENIED") {
		t.Fatalf("error text=%#v", result.Content[0])
	}
}

func TestBindSessionAndConsolidateRoundTrip(t *testing.T) {
	useCases := &fakeUseCases{}
	session := connect(t, testHandler(t, useCases, testIdentity("session", "consolidate")).URL, testSecret)
	bound, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_bind_session", Arguments: map[string]any{"external_id": "ext-1", "title": "chat"}})
	if err != nil || bound.IsError {
		t.Fatalf("bind_session=%#v err=%v", bound, err)
	}
	if structured, ok := bound.StructuredContent.(map[string]any); !ok || structured["session_id"] != "session-a" {
		t.Fatalf("bind structured=%#v", bound.StructuredContent)
	}
	consolidated, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "memory_consolidate", Arguments: map[string]any{"scope": "user-global"}})
	if err != nil || consolidated.IsError {
		t.Fatalf("consolidate=%#v err=%v", consolidated, err)
	}
	if len(useCases.consolidated) != 1 {
		t.Fatalf("consolidated=%d", len(useCases.consolidated))
	}
}

func TestEnvelopeRejectsSessionScopeWithoutSessionID(t *testing.T) {
	h := &Handler{}
	if _, err := h.envelope(testIdentity("observe"), "", "", "session", "key-1", map[string]any{}, nil); err == nil {
		t.Fatal("session scope without session_id must fail")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
