package knowledge

import "testing"

// TestLexicalRow: the row carries the identity the legs fuse on, the text it is
// scored on, and its owner as the index's user — "" for the org's own.
func TestLexicalRow(t *testing.T) {
	row := lexicalRow(DTMemory, "m1", map[string]any{"title": "api rotation", "content": "rotate monthly", "owner": "alice", "project": "p"})
	if row["key"] != "kb.memory/m1" || row["doctype"] != "kb.memory" || row["name"] != "m1" {
		t.Fatalf("identity: %v", row)
	}
	if row["text"] != "api rotation\n\nrotate monthly" || row["user"] != "alice" || row["project"] != "p" {
		t.Fatalf("text/user/project: %v", row)
	}
	if _, has := row["url"]; has {
		t.Fatal("an absent field is absent, not empty")
	}
	if lexicalRow(DTSource, "s", map[string]any{"title": "t", "body": "b"})["user"] != "" {
		t.Fatal("the org's own document has no user")
	}
}
