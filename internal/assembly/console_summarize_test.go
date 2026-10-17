package assembly

import (
	"encoding/json"
	"testing"
)

// TestSummarizeRendersStructuredContent guards the console read path: a node
// whose content is a structured object with no explicit text field must still
// render a non-empty summary instead of a blank list item.
func TestSummarizeRendersStructuredContent(t *testing.T) {
	content := json.RawMessage(`{"name":"log-trace-mcp","type":"project_overview","status":"blocked on live E2E"}`)
	got := summarize(content, "")
	if got == "" {
		t.Fatalf("structured content rendered empty")
	}
	if len([]rune(got)) > 200 {
		t.Fatalf("summary length = %d, want <= 200", len([]rune(got)))
	}
}

func TestSummarizeFallsBackToStoredText(t *testing.T) {
	if got := summarize(json.RawMessage(`{}`), "legacy stored text"); got != "legacy stored text" {
		t.Fatalf("summarize fallback = %q", got)
	}
}

func TestSummarizeUsesSemanticField(t *testing.T) {
	if got := summarize(json.RawMessage(`{"summary":"the build needs GOPROXY"}`), ""); got != "the build needs GOPROXY" {
		t.Fatalf("summarize semantic = %q", got)
	}
}
