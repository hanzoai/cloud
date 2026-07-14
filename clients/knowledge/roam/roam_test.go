package roam

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

func TestParse_RoamExport(t *testing.T) {
	data, err := os.ReadFile("testdata/roam.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	pages, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("want 2 pages, got %d", len(pages))
	}

	atlas, ok := byTitle(pages, "Project Atlas")
	if !ok {
		t.Fatalf("missing Project Atlas")
	}
	if atlas.Slug != "project-atlas" {
		t.Errorf("slug = %q, want project-atlas", atlas.Slug)
	}
	// Wikilinks, tags, and block refs preserved verbatim so the KB hook extracts links.
	for _, want := range []string{"[[Design System]]", "[[API Spec]]", "#alice", "((k9fA2bQ))"} {
		if !strings.Contains(atlas.Body, want) {
			t.Errorf("body missing %q: %s", want, atlas.Body)
		}
	}
	// Nested block text made it in.
	if !strings.Contains(atlas.Body, "Blocked until") {
		t.Errorf("nested block dropped: %s", atlas.Body)
	}
	if !strings.HasPrefix(atlas.Body, `{"root"`) {
		t.Errorf("body not Lexical JSON")
	}
}

func TestParse_SingleObject(t *testing.T) {
	pages, err := Parse([]byte(`{"title":"Solo","children":[{"string":"one [[Two]]"}]}`))
	if err != nil {
		t.Fatalf("Parse single: %v", err)
	}
	if len(pages) != 1 || pages[0].Title != "Solo" {
		t.Fatalf("single-object export not handled: %+v", pages)
	}
	if !strings.Contains(pages[0].Body, "[[Two]]") {
		t.Errorf("wikilink lost: %s", pages[0].Body)
	}
}

func TestParse_Invalid(t *testing.T) {
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("expected error on invalid JSON")
	}
}
