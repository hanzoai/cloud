package knowledge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// helpers.go holds the small, pure, dependency-free utilities the KB lane shares.
// No I/O, no framework, no HTTP — trivially testable in plain Go.

// Sentinel errors let the Qdrant REST layer branch on "not found" / "already
// exists" without string-matching at the call sites (ensureCollection).
var (
	errNotFound = errors.New("kb: qdrant resource not found")
	errConflict = errors.New("kb: qdrant resource already exists")
)

func isNotFound(err error) bool { return errors.Is(err, errNotFound) }
func isConflict(err error) bool { return errors.Is(err, errConflict) }

// str coerces a JSON-decoded value to a string (the framework stores document data
// as map[string]any). A non-string is rendered empty rather than guessed, so a
// numeric/absent field never becomes a bogus text value.
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// parseInt parses a base-10 int, trimming surrounding space.
func parseInt(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

// b64Decode decodes a base64 payload, tolerating the newline-wrapped standard
// encoding GitHub's contents API returns (it embeds "\n" every 60 chars).
func b64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(s, "\n", ""))
}

// truncate returns at most n bytes of b as a string (for bounded error messages).
func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

// lexicalText extracts the plain text runs from a Lexical EditorState JSON string
// (the RichText fieldtype's stored value), so the embedding is over PROSE, not
// editor structure. It walks the node tree collecting every `text` string and
// inserting newlines at block boundaries. A value that isn't Lexical JSON (e.g.
// plain markdown a caller stored) is returned as-is — the function degrades to
// identity rather than dropping content.
func lexicalText(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var root struct {
		Root json.RawMessage `json:"root"`
	}
	if err := json.Unmarshal([]byte(body), &root); err != nil || len(root.Root) == 0 {
		return body // not Lexical JSON — treat the raw value as text
	}
	var b strings.Builder
	collectLexical(root.Root, &b)
	out := strings.TrimSpace(b.String())
	if out == "" {
		return body
	}
	return out
}

// collectLexical recursively appends the `text` of every node in a Lexical subtree,
// separating block-level nodes with a newline so paragraphs don't run together.
func collectLexical(raw json.RawMessage, b *strings.Builder) {
	var node struct {
		Type     string            `json:"type"`
		Text     string            `json:"text"`
		Children []json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return
	}
	if node.Text != "" {
		b.WriteString(node.Text)
	}
	for _, ch := range node.Children {
		collectLexical(ch, b)
	}
	// A block-level node (paragraph/heading/list-item/quote) ends a line so adjacent
	// blocks embed as distinct prose rather than one concatenated string.
	switch node.Type {
	case "paragraph", "heading", "listitem", "quote", "code":
		b.WriteString("\n")
	}
}
