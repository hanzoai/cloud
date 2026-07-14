package evernote

import (
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

func byTitle(pages []vault.Page, title string) (vault.Page, bool) {
	for _, p := range pages {
		if p.Title == title {
			return p, true
		}
	}
	return vault.Page{}, false
}

func TestParse_Enex(t *testing.T) {
	data, err := os.ReadFile("testdata/export.enex")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	pages, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("want 2 pages, got %d: %+v", len(pages), pages)
	}

	mn, ok := byTitle(pages, "Meeting Notes")
	if !ok {
		t.Fatalf("missing Meeting Notes")
	}
	if mn.Slug != "meeting-notes" {
		t.Errorf("slug = %q, want meeting-notes", mn.Slug)
	}
	if !strings.HasPrefix(mn.Body, `{"root"`) {
		t.Errorf("body not Lexical JSON: %s", mn.Body)
	}
	// ENML div-per-line text and list items survive; a wikilink is preserved for the
	// KB hook (Evernote has no native wikilinks, but authored "[[…]]" text must pass).
	for _, want := range []string{"Discussed the", "[[Roadmap]]", "Action items", "Ship the connector", "Write the docs"} {
		if !flatContains(mn.Body, want) {
			t.Errorf("body missing %q: %s", want, mn.Body)
		}
	}
}

// flatContains checks the substring appears in the Lexical JSON's text (the JSON
// stores text verbatim inside "text" values, so a plain Contains suffices here).
func flatContains(body, sub string) bool { return strings.Contains(body, sub) }

func TestParse_Invalid(t *testing.T) {
	if _, err := Parse([]byte(`<not-valid`)); err == nil {
		t.Error("expected error on malformed enex")
	}
}
