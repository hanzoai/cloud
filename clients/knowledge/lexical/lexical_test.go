package lexical

import (
	"encoding/json"
	"strings"
	"testing"
)

// flatten mirrors clients/knowledge.lexicalText: it walks a Lexical EditorState and
// concatenates every text node, so a test can assert content survives the build
// (the KB indexer + wikilink extractor read exactly this way).
func flatten(t *testing.T, editorState string) string {
	t.Helper()
	var root struct {
		Root json.RawMessage `json:"root"`
	}
	if err := json.Unmarshal([]byte(editorState), &root); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	var b strings.Builder
	var walk func(json.RawMessage)
	walk = func(raw json.RawMessage) {
		var n struct {
			Text     string            `json:"text"`
			Children []json.RawMessage `json:"children"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			return
		}
		b.WriteString(n.Text)
		for _, c := range n.Children {
			walk(c)
		}
		b.WriteString(" ")
	}
	walk(root.Root)
	return b.String()
}

func TestBuild_EmptyYieldsValidDoc(t *testing.T) {
	out := Build(nil)
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("empty build not valid JSON: %v", err)
	}
	if _, ok := v["root"]; !ok {
		t.Fatalf("missing root: %s", out)
	}
}

func TestFromMarkdown_Blocks(t *testing.T) {
	md := "# Title\n\nA paragraph with a [[Wikilink]] inside.\n\n- one\n- two\n\n> a quote\n\n```\ncode line\n```\n\n1. first\n2. second"
	out := FromMarkdown(md)

	var doc struct {
		Root struct {
			Children []struct {
				Type     string `json:"type"`
				Tag      string `json:"tag"`
				ListType string `json:"listType"`
			} `json:"children"`
		} `json:"root"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	types := make([]string, 0, len(doc.Root.Children))
	for _, c := range doc.Root.Children {
		types = append(types, c.Type)
	}
	want := []string{"heading", "paragraph", "list", "quote", "code", "list"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("block types = %v, want %v", types, want)
	}
	if doc.Root.Children[0].Tag != "h1" {
		t.Errorf("heading tag = %q, want h1", doc.Root.Children[0].Tag)
	}
	if doc.Root.Children[2].ListType != "bullet" || doc.Root.Children[5].ListType != "number" {
		t.Errorf("list types wrong: %+v", doc.Root.Children)
	}
	// The wikilink must survive verbatim so the KB hook can extract it.
	if !strings.Contains(flatten(t, out), "[[Wikilink]]") {
		t.Errorf("wikilink not preserved in %s", flatten(t, out))
	}
}

func TestFromHTML_BlocksAndWikilink(t *testing.T) {
	htmlDoc := `<html><body>
	<h2>Heading</h2>
	<p>Text with <a href="x.html">an anchor</a> and a [[Ref]] link.</p>
	<ul><li>alpha</li><li>beta</li></ul>
	</body></html>`
	out := FromHTML(htmlDoc)
	flat := flatten(t, out)
	if !strings.Contains(flat, "Heading") || !strings.Contains(flat, "an anchor") {
		t.Errorf("html text lost: %q", flat)
	}
	// Anchor text kept, and inter-node spacing preserved ("Text with an anchor and").
	if !strings.Contains(flat, "Text with an anchor and a [[Ref]] link.") {
		t.Errorf("inline spacing/wikilink wrong: %q", flat)
	}
	if !strings.Contains(flat, "alpha") || !strings.Contains(flat, "beta") {
		t.Errorf("list items lost: %q", flat)
	}
}

func TestFromPlain_Paragraphs(t *testing.T) {
	out := FromPlain("para one\nline two\n\npara two")
	var doc struct {
		Root struct {
			Children []struct {
				Type string `json:"type"`
			} `json:"children"`
		} `json:"root"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(doc.Root.Children) != 2 {
		t.Fatalf("want 2 paragraphs, got %d", len(doc.Root.Children))
	}
}
