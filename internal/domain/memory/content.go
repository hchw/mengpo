package memory

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// DefaultContentTextLimit bounds a rendered projection. content_text feeds
// full-text search, embeddings, rerank and the console, so it must stay a
// readable, bounded string rather than an unbounded JSON dump.
const DefaultContentTextLimit = 4000

// semanticContentKeys are content fields whose string value is already the
// human-readable text of the memory. They win over any structural rendering.
var semanticContentKeys = []string{"summary", "text", "content", "message"}

// RenderContentText returns the canonical, deterministic text projection of a
// memory node's content. It is the single source of truth shared by writers
// (candidate persistence, triage) and readers (console), so a node with content
// is never a "ghost" that exists but cannot be searched, embedded or shown.
//
// The rendering never infers or invents text; it only projects what the content
// already states:
//
//  1. a semantic text field (summary/text/content/message) is used as-is;
//  2. otherwise the JSON is flattened into stable, key-sorted "key: value"
//     lines built from scalar leaves;
//  3. content that truly holds no readable text renders as an empty string.
func RenderContentText(content json.RawMessage) string {
	return renderContentText(content, DefaultContentTextLimit)
}

func renderContentText(content json.RawMessage, limit int) string {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	if text, ok := semanticText(value); ok {
		return truncateContentText(text, limit)
	}
	lines := make([]string, 0, 8)
	flattenContent("", value, &lines)
	if len(lines) == 0 {
		return ""
	}
	return truncateContentText(strings.Join(lines, "\n"), limit)
}

// semanticText resolves a top-level string, or a top-level object's first
// non-empty semantic key, to readable text.
func semanticText(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		return text, text != ""
	case map[string]any:
		for _, key := range semanticContentKeys {
			if raw, ok := typed[key]; ok {
				if text, ok := raw.(string); ok {
					trimmed := strings.TrimSpace(text)
					if trimmed != "" {
						return trimmed, true
					}
				}
			}
		}
	}
	return "", false
}

// flattenContent appends scalar leaves of value to lines in a stable order.
func flattenContent(prefix string, value any, lines *[]string) {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			*lines = append(*lines, contentLine(prefix, text))
		}
	case json.Number:
		*lines = append(*lines, contentLine(prefix, typed.String()))
	case float64:
		*lines = append(*lines, contentLine(prefix, strconv.FormatFloat(typed, 'g', -1, 64)))
	case bool:
		*lines = append(*lines, contentLine(prefix, strconv.FormatBool(typed)))
	case []any:
		scalars := make([]string, 0, len(typed))
		nested := make([]any, 0, len(typed))
		for _, item := range typed {
			switch item.(type) {
			case map[string]any, []any:
				nested = append(nested, item)
			default:
				var itemLines []string
				flattenContent("", item, &itemLines)
				scalars = append(scalars, itemLines...)
			}
		}
		if len(scalars) > 0 {
			*lines = append(*lines, contentLine(prefix, strings.Join(scalars, ", ")))
		}
		for index, item := range nested {
			child := prefix
			suffix := "[" + strconv.Itoa(index) + "]"
			if child == "" {
				child = suffix
			} else {
				child += suffix
			}
			flattenContent(child, item, lines)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if prefix != "" {
				child = prefix + "." + key
			}
			flattenContent(child, typed[key], lines)
		}
	}
}

func contentLine(prefix, text string) string {
	if prefix == "" {
		return text
	}
	return prefix + ": " + text
}

// truncateContentText cuts to limit runes so multi-byte text stays valid.
func truncateContentText(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
