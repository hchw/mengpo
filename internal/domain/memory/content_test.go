package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderContentText(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{name: "summary key wins", content: `{"summary":"the build needs GOPROXY"}`, want: "the build needs GOPROXY"},
		{name: "text key", content: `{"text":"remember this"}`, want: "remember this"},
		{name: "content string key", content: `{"content":"body"}`, want: "body"},
		{name: "message key", content: `{"message":"hi"}`, want: "hi"},
		{name: "plain string", content: `"standalone"`, want: "standalone"},
		{
			name:    "structured object flattens in stable key order",
			content: `{"status":"blocked","name":"log-trace-mcp","deps":3,"ok":true}`,
			want:    "deps: 3\nname: log-trace-mcp\nok: true\nstatus: blocked",
		},
		{name: "nested object uses dotted path", content: `{"env":{"B":"2","A":"1"}}`, want: "env.A: 1\nenv.B: 2"},
		{name: "array of scalars joins", content: `{"tags":["a","b"]}`, want: "tags: a, b"},
		{name: "empty object", content: `{}`, want: ""},
		{name: "empty array", content: `[]`, want: ""},
		{name: "blank semantic value falls through to empty", content: `{"summary":"   "}`, want: ""},
		{name: "null content", content: `null`, want: ""},
		{name: "zero bytes", content: ``, want: ""},
		{name: "invalid json", content: `{`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderContentText(json.RawMessage(tc.content))
			if got != tc.want {
				t.Fatalf("RenderContentText(%s) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestRenderContentTextIsDeterministic(t *testing.T) {
	content := json.RawMessage(`{"z":1,"a":{"y":2,"x":3},"m":"text"}`)
	first := RenderContentText(content)
	second := RenderContentText(content)
	if first != second {
		t.Fatalf("render is not deterministic: %q != %q", first, second)
	}
	if !strings.Contains(first, "a.x: 3") || !strings.Contains(first, "a.y: 2") {
		t.Fatalf("nested keys missing from %q", first)
	}
}

func TestRenderContentTextTruncatesByRune(t *testing.T) {
	long := strings.Repeat("界", DefaultContentTextLimit+50)
	content, err := json.Marshal(map[string]string{"summary": long})
	if err != nil {
		t.Fatal(err)
	}
	got := RenderContentText(content)
	if len([]rune(got)) != DefaultContentTextLimit {
		t.Fatalf("rendered rune length = %d, want %d", len([]rune(got)), DefaultContentTextLimit)
	}
}
