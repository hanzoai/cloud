package notion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

func readExport(t *testing.T, root string) []vault.File {
	t.Helper()
	var files []vault.File
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, vault.File{Path: filepath.ToSlash(rel), Data: b})
		return nil
	})
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	return files
}

func byTitle(pages []vault.Page, title string) (vault.Page, bool) {
	for _, p := range pages {
		if p.Title == title {
			return p, true
		}
	}
	return vault.Page{}, false
}

// TestParseExport_Markdown proves the id is stripped from titles, folder nesting
// becomes the parent tree, intra-export links become "[[Title]]" wikilinks, and
// external links are left alone.
func TestParseExport_Markdown(t *testing.T) {
	pages, err := ParseExport(readExport(t, "testdata/export"))
	if err != nil {
		t.Fatalf("ParseExport: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("want 2 pages, got %d: %+v", len(pages), pages)
	}

	eng, ok := byTitle(pages, "Engineering")
	if !ok {
		t.Fatalf("missing Engineering (id not stripped?): %+v", pages)
	}
	run, ok := byTitle(pages, "Runbook")
	if !ok {
		t.Fatalf("missing Runbook")
	}

	// Parent tree from folder nesting.
	if run.Parent != eng.Slug {
		t.Errorf("Runbook.Parent = %q, want %q", run.Parent, eng.Slug)
	}
	if eng.Parent != "" {
		t.Errorf("Engineering must be a root, got parent %q", eng.Parent)
	}

	// Intra-export link rewritten to a wikilink; external link untouched.
	if !strings.Contains(eng.Body, "[[Runbook]]") {
		t.Errorf("Engineering body missing [[Runbook]]: %s", eng.Body)
	}
	if !strings.Contains(eng.Body, "https://www.notion.so") {
		t.Errorf("external link should survive: %s", eng.Body)
	}
	// Back-link (relative "../") resolved too.
	if !strings.Contains(run.Body, "[[Engineering]]") {
		t.Errorf("Runbook body missing [[Engineering]]: %s", run.Body)
	}
}

// TestParseExport_HTML proves the HTML export path rewrites <a href> links to
// wikilinks and keeps block text.
func TestParseExport_HTML(t *testing.T) {
	files := []vault.File{
		{Path: "Home 11111111111111111111111111111111.html", Data: []byte(
			`<html><body><h1 class="page-title">Home</h1>
			<p>Go to <a href="Notes%2022222222222222222222222222222222.html">notes</a> now.</p></body></html>`,
		)},
		{Path: "Notes 22222222222222222222222222222222.html", Data: []byte(
			`<html><body><h1 class="page-title">Notes</h1><p>Some notes.</p></body></html>`,
		)},
	}
	pages, err := ParseExport(files)
	if err != nil {
		t.Fatalf("ParseExport: %v", err)
	}
	home, ok := byTitle(pages, "Home")
	if !ok {
		t.Fatalf("missing Home page: %+v", pages)
	}
	if !strings.Contains(home.Body, "[[Notes]]") {
		t.Errorf("HTML link not rewritten to wikilink: %s", home.Body)
	}
	if !strings.Contains(home.Body, "Go to") || !strings.Contains(home.Body, "now.") {
		t.Errorf("surrounding text lost: %s", home.Body)
	}
}
